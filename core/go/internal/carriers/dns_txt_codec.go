package carriers

import (
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"strings"
)

// Minimal DNS wire codec for the dns.txt carrier. The carrier needs exactly
// one query shape (TXT), one answer type and name parsing that tolerates 0x20
// case randomization, so it hand-rolls the codec instead of pulling in a DNS
// library.

const (
	dnsTypeTXT        = 16
	dnsTypeOPT        = 41
	dnsClassIN        = 1
	dnsFlagQR         = 0x8000
	dnsFlagTC         = 0x0200
	dnsFlagRD         = 0x0100
	dnsFlagRA         = 0x0080
	dnsRcodeOK        = 0
	dnsRcodeFormatErr = 1
	dnsRcodeNXDomain  = 3
	dnsRcodeServFail  = 2
	dnsRcodeRefused   = 5

	// dnsTXTHelloPrefix starts every hello TXT answer so clients can confirm
	// the tunnel exit (and not an unrelated wildcard) answered the probe.
	dnsTXTHelloPrefix = "wtdns1"
	// dnsTXTMaxLabel keeps every encoded data label far below the 63-byte
	// DNS label limit so resolvers never truncate query names.
	dnsTXTMaxLabel = 56
	// dnsTXTMaxQueryName is the wire-format qname budget (255 including the
	// length bytes, 253 visible characters).
	dnsTXTMaxQueryName = 253
)

var dnsB32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// dnsBuildTXTQuery encodes a TXT query with EDNS0 advertised at 1232 bytes.
func dnsBuildTXTQuery(id uint16, qname string, recursionDesired bool) ([]byte, error) {
	wireName, err := dnsEncodeName(qname)
	if err != nil {
		return nil, err
	}
	// Public recursive resolvers answer RD=0 queries with REFUSED; the
	// recursion-desired bit is what routes the query through them.
	flags := uint16(0)
	if recursionDesired {
		flags |= dnsFlagRD
	}
	query := make([]byte, 0, 12+len(wireName)+11+16)
	query = append(query,
		byte(id>>8), byte(id),
		byte(flags>>8), byte(flags),
		0, 1, // QDCOUNT
		0, 0, // ANCOUNT
		0, 0, // NSCOUNT
		0, 1, // ARCOUNT: OPT
	)
	query = append(query, wireName...)
	query = append(query, byte(dnsTypeTXT>>8), byte(dnsTypeTXT), 0, byte(dnsClassIN))
	// OPT root name + fixed fields advertising a 1232-byte UDP buffer.
	query = append(query, 0, byte(dnsTypeOPT>>8), byte(dnsTypeOPT), 0x04, 0xd0, 0, 0, 0, 0, 0, 0)
	return query, nil
}

// dnsEncodeName converts a dotted name to wire format.
func dnsEncodeName(name string) ([]byte, error) {
	trimmed := strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	if trimmed == "" {
		return nil, errors.New("dns name is empty")
	}
	if len(trimmed) > dnsTXTMaxQueryName {
		return nil, fmt.Errorf("dns name too long (%d > %d): %q", len(trimmed), dnsTXTMaxQueryName, trimmed)
	}
	out := make([]byte, 0, len(trimmed)+2)
	for _, label := range strings.Split(trimmed, ".") {
		if label == "" {
			return nil, fmt.Errorf("dns name has empty label: %q", name)
		}
		if len(label) > 63 {
			return nil, fmt.Errorf("dns label too long (%d): %q", len(label), label)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), nil
}

// dnsTXTAnswer is one TXT resource record.
type dnsTXTAnswer struct {
	Strings []string
}

// dnsParsedResponse carries the fields the carrier cares about.
type dnsParsedResponse struct {
	ID        uint16
	RCode     int
	Truncated bool
	Answers   []dnsTXTAnswer
}

// dnsParseResponse decodes a DNS response header, question section and TXT
// answers. Unknown answer types are skipped.
func dnsParseResponse(data []byte) (dnsParsedResponse, error) {
	if len(data) < 12 {
		return dnsParsedResponse{}, errors.New("dns response shorter than header")
	}
	flags := int(data[2])<<8 | int(data[3])
	out := dnsParsedResponse{
		ID:        uint16(data[0])<<8 | uint16(data[1]),
		RCode:     flags & 0x0f,
		Truncated: flags&dnsFlagTC != 0,
	}
	qdCount := int(data[4])<<8 | int(data[5])
	anCount := int(data[6])<<8 | int(data[7])
	offset := 12
	for i := 0; i < qdCount; i++ {
		next, err := dnsSkipName(data, offset)
		if err != nil {
			return dnsParsedResponse{}, err
		}
		if next+4 > len(data) {
			return dnsParsedResponse{}, errors.New("dns question truncated")
		}
		offset = next + 4
	}
	for i := 0; i < anCount; i++ {
		next, err := dnsSkipName(data, offset)
		if err != nil {
			return dnsParsedResponse{}, err
		}
		if next+10 > len(data) {
			return dnsParsedResponse{}, errors.New("dns answer truncated")
		}
		rrType := int(data[next])<<8 | int(data[next+1])
		rdLen := int(data[next+8])<<8 | int(data[next+9])
		rdataStart := next + 10
		if rdataStart+rdLen > len(data) {
			return dnsParsedResponse{}, errors.New("dns rdata truncated")
		}
		if rrType == dnsTypeOPT {
			offset = rdataStart + rdLen
			continue
		}
		if rrType == dnsTypeTXT {
			strings_, err := dnsParseTXTStrings(data[rdataStart : rdataStart+rdLen])
			if err != nil {
				return dnsParsedResponse{}, err
			}
			out.Answers = append(out.Answers, dnsTXTAnswer{Strings: strings_})
		}
		offset = rdataStart + rdLen
	}
	return out, nil
}

// dnsParseTXTStrings decodes the character-string sequence of a TXT RDATA
// block.
func dnsParseTXTStrings(rdata []byte) ([]string, error) {
	var out []string
	for offset := 0; offset < len(rdata); {
		length := int(rdata[offset])
		if offset+1+length > len(rdata) {
			return nil, errors.New("dns txt string truncated")
		}
		out = append(out, string(rdata[offset+1:offset+1+length]))
		offset += 1 + length
	}
	return out, nil
}

// dnsSkipName walks one (possibly compressed) domain name and returns the
// offset just past it in the original stream. Following a compression
// pointer does not advance the return offset: the name ends right after
// the two pointer bytes.
func dnsSkipName(data []byte, offset int) (int, error) {
	end := -1
	jumps := 0
	for {
		if offset >= len(data) {
			return 0, errors.New("dns name runs past packet")
		}
		length := int(data[offset])
		if length == 0 {
			if end == -1 {
				end = offset + 1
			}
			return end, nil
		}
		if length&0xc0 == 0xc0 {
			if offset+2 > len(data) {
				return 0, errors.New("dns compression pointer truncated")
			}
			if end == -1 {
				end = offset + 2
			}
			jumps++
			if jumps > 8 {
				return 0, errors.New("dns compression loop")
			}
			offset = int(data[offset+1]&0x3f) | int(data[offset]&0x3f)<<8
			continue
		}
		if length > 63 {
			return 0, errors.New("dns label too long")
		}
		offset += 1 + length
	}
}

// dnsBuildTXTResponse builds an answer for one query. A nil strings slice
// with rcode OK produces an empty NOERROR answer; the question section is
// echoed verbatim so random query-name casing survives the round trip.
func dnsBuildTXTResponse(query []byte, strings []string, rcode int) []byte {
	if len(query) < 12 {
		return nil
	}
	id := int(query[0])<<8 | int(query[1])
	questionEnd, err := dnsSkipName(query, 12)
	if err != nil || questionEnd+4 > len(query) {
		rcode = dnsRcodeFormatErr
		questionEnd = 12
	}
	question := query[12 : questionEnd+4]
	flags := dnsFlagQR | dnsFlagRD | dnsFlagRA | rcode
	answers := 0
	var rdata []byte
	if len(strings) > 0 {
		answers = 1
		for _, s := range strings {
			if len(s) > 255 {
				s = s[:255]
			}
			rdata = append(rdata, byte(len(s)))
			rdata = append(rdata, s...)
		}
	}
	out := make([]byte, 0, 12+len(question)+16+len(rdata))
	out = append(out, byte(id>>8), byte(id),
		byte(flags>>8), byte(flags&0xff),
		0, 1,
		byte(answers>>8), byte(answers),
		0, 0,
		0, 0)
	out = append(out, question...)
	if answers > 0 {
		out = append(out, 0xc0, 0x0c, // name pointer to question
			byte(dnsTypeTXT>>8), byte(dnsTypeTXT),
			0, byte(dnsClassIN),
			0, 0, 0, 0, // TTL 0: never cache tunnel payloads
			byte(len(rdata)>>8), byte(len(rdata)))
		out = append(out, rdata...)
	}
	return out
}

// dnsTXTQueryID returns the query transaction id embedded in raw bytes.
func dnsTXTQueryID(query []byte) uint16 {
	if len(query) < 2 {
		return 0
	}
	return uint16(query[0])<<8 | uint16(query[1])
}

// dnsSplitB32Labels splits a base32 string into DNS-safe labels.
func dnsSplitB32Labels(encoded string) []string {
	var labels []string
	for offset := 0; offset < len(encoded); offset += dnsTXTMaxLabel {
		end := offset + dnsTXTMaxLabel
		if end > len(encoded) {
			end = len(encoded)
		}
		labels = append(labels, encoded[offset:end])
	}
	return labels
}

// dnsRandomCase randomly flips the case of ASCII letters in s. Combined with
// TTL 0 answers this defeats resolver caches on retransmissions.
func dnsRandomCase(s string, rng *rand.Rand) string {
	if rng == nil {
		return s
	}
	runes := []byte(s)
	for i := range runes {
		c := runes[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if rng.Intn(2) == 0 {
				runes[i] = c ^ 0x20
			}
		}
	}
	return string(runes)
}

// dnsEncodeUpChunk encodes raw upstream bytes as lowercase base32.
func dnsEncodeUpChunk(data []byte) string {
	return strings.ToLower(dnsB32Encoding.EncodeToString(data))
}

// dnsDecodeUpChunk decodes case-insensitive base32 back to raw bytes.
func dnsDecodeUpChunk(encoded string) ([]byte, error) {
	return dnsB32Encoding.DecodeString(strings.ToUpper(encoded))
}

// dnsEncodeDownChunk encodes downstream TXT payload bytes.
func dnsEncodeDownChunk(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// dnsDecodeDownChunk decodes downstream TXT payload bytes.
func dnsDecodeDownChunk(encoded string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(encoded)
}
