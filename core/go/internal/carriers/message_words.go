package carriers

import (
	cryptorand "crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	vkCipherWordBits  = 13
	vkCipherWordCount = 1 << vkCipherWordBits
	vkCipherWordMask  = vkCipherWordCount - 1
	maxCipherWords    = 10_000

	socialWordFragmentHeaderLen = 23
	// VK/OK readers request a bounded history page. Keep one envelope below
	// that page size so its parts can be assembled during one poll.
	socialWordFragmentMaxParts  = 64
	socialWordFragmentMaxGroups = 32
	socialWordFragmentTTL       = 10 * time.Minute
)

// vkcipherWordsV4 is the byte-identical ru-common-8192-v4 dictionary from
// github.com/megamen32/vkciphr at 32084d9. It is MIT-licensed; the local
// attribution is kept in LICENSE.vkcipher-words. VKCipher uses it to turn an
// opaque byte stream into 13-bit Russian words. WhiteTransport applies that
// exact transport encoding only after its existing authenticated envelope
// cipher has sealed the payload.
//
//go:embed vkcipher_words_v4.txt
var vkcipherWordsV4 string

var vkcipherDictionary struct {
	once    sync.Once
	words   []string
	indexes map[string]uint16
	err     error
}

func loadVKCipherDictionary() ([]string, map[string]uint16, error) {
	vkcipherDictionary.once.Do(func() {
		words := strings.Fields(vkcipherWordsV4)
		if len(words) != vkCipherWordCount {
			vkcipherDictionary.err = fmt.Errorf("vkcipher dictionary has %d words, want %d", len(words), vkCipherWordCount)
			return
		}
		indexes := make(map[string]uint16, len(words))
		for index, word := range words {
			if _, exists := indexes[word]; exists {
				vkcipherDictionary.err = fmt.Errorf("vkcipher dictionary contains duplicate word %q", word)
				return
			}
			indexes[word] = uint16(index)
		}
		vkcipherDictionary.words = words
		vkcipherDictionary.indexes = indexes
	})
	return vkcipherDictionary.words, vkcipherDictionary.indexes, vkcipherDictionary.err
}

// encodeVKCipherWords applies VKCipher's lossless length-prefixed, 13-bit
// word packing. It intentionally does not add a marker: a valid encrypted
// WhiteTransport envelope remains the authentication gate on receive.
func encodeVKCipherWords(payload []byte) (string, error) {
	words, _, err := loadVKCipherDictionary()
	if err != nil {
		return "", err
	}
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return "", fmt.Errorf("vkcipher word payload too large: %d", len(payload))
	}

	source := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(source, uint32(len(payload)))
	copy(source[4:], payload)

	var encoded strings.Builder
	var accumulator uint32
	var bitCount uint
	for _, b := range source {
		accumulator = accumulator<<8 | uint32(b)
		bitCount += 8
		for bitCount >= vkCipherWordBits {
			bitCount -= vkCipherWordBits
			if encoded.Len() > 0 {
				encoded.WriteByte(' ')
			}
			encoded.WriteString(words[(accumulator>>bitCount)&vkCipherWordMask])
			if bitCount == 0 {
				accumulator = 0
			} else {
				accumulator &= (1 << bitCount) - 1
			}
		}
	}
	if bitCount > 0 {
		if encoded.Len() > 0 {
			encoded.WriteByte(' ')
		}
		encoded.WriteString(words[(accumulator<<(vkCipherWordBits-bitCount))&vkCipherWordMask])
	}
	return encoded.String(), nil
}

// decodeVKCipherWords returns candidate=false for ordinary provider text. A
// word-only message is a candidate and must be fully well-formed; malformed
// candidates are rejected rather than falling through as a plaintext envelope.
func decodeVKCipherWords(text string) (payload []byte, candidate bool, err error) {
	_, indexes, err := loadVKCipherDictionary()
	if err != nil {
		return nil, false, err
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil, false, nil
	}
	if len(words) > maxCipherWords {
		return nil, true, fmt.Errorf("vkcipher word ciphertext has %d words, exceeds %d", len(words), maxCipherWords)
	}

	indexesForWords := make([]uint16, len(words))
	for position, word := range words {
		index, exists := indexes[word]
		if !exists {
			return nil, false, nil
		}
		indexesForWords[position] = index
	}

	decoded := make([]byte, 0, (len(words)*vkCipherWordBits)/8)
	var accumulator uint32
	var bitCount uint
	for _, index := range indexesForWords {
		accumulator = accumulator<<vkCipherWordBits | uint32(index)
		bitCount += vkCipherWordBits
		for bitCount >= 8 {
			bitCount -= 8
			decoded = append(decoded, byte(accumulator>>bitCount))
			if bitCount == 0 {
				accumulator = 0
			} else {
				accumulator &= (1 << bitCount) - 1
			}
		}
	}
	if bitCount > 0 && accumulator != 0 {
		return nil, true, fmt.Errorf("vkcipher word ciphertext has non-zero padding")
	}
	if len(decoded) < 4 {
		return nil, true, fmt.Errorf("vkcipher word ciphertext is too short")
	}
	payloadLength := uint64(binary.BigEndian.Uint32(decoded[:4]))
	expectedLength := payloadLength + 4
	if expectedLength > uint64(len(decoded)) {
		return nil, true, fmt.Errorf("vkcipher word ciphertext length %d exceeds decoded bytes %d", payloadLength, len(decoded)-4)
	}
	for _, b := range decoded[expectedLength:] {
		if b != 0 {
			return nil, true, fmt.Errorf("vkcipher word ciphertext has non-zero trailing bits")
		}
	}
	return decoded[4:expectedLength], true, nil
}

var socialWordFragmentMagic = [4]byte{'W', 'T', 'W', '1'}

type socialWordFragment struct {
	groupID   [12]byte
	partIndex uint16
	partCount uint16
	payload   []byte
}

type socialWordFragmentGroup struct {
	partCount uint16
	parts     map[uint16][]byte
	expiresAt time.Time
}

type socialWordFragmentAssembler struct {
	mu     sync.Mutex
	groups map[string]socialWordFragmentGroup
}

// encodeSocialMessageEnvelopes emits one word ciphertext when it fits the
// carrier's text limit and otherwise creates self-contained word fragments.
// The fragment payload is still the pre-existing AES-GCM-authenticated
// wtenc1 envelope; fragment metadata alone cannot produce a valid envelope.
func encodeSocialMessageEnvelopes(envelope fabric.Envelope, messageLimit int, cipher ...*fabric.EnvelopeCipher) ([]string, error) {
	text, err := encodeMailboxEnvelope(envelope, cipher...)
	if err != nil {
		return nil, err
	}
	if len(cipher) == 0 || cipher[0] == nil {
		return []string{text}, nil
	}
	encoded, err := encodeVKCipherWords([]byte(text))
	if err != nil {
		return nil, err
	}
	if messageLimit <= 0 || len(encoded) <= messageLimit {
		return []string{encoded}, nil
	}

	payloadLimit, err := socialWordFragmentPayloadLimit(messageLimit)
	if err != nil {
		return nil, err
	}
	raw := []byte(text)
	partCount := (len(raw) + payloadLimit - 1) / payloadLimit
	if partCount > socialWordFragmentMaxParts {
		return nil, fmt.Errorf("social word ciphertext needs %d fragments, exceeds %d", partCount, socialWordFragmentMaxParts)
	}
	var groupID [12]byte
	if _, err := cryptorand.Read(groupID[:]); err != nil {
		return nil, fmt.Errorf("generate social word fragment group: %w", err)
	}
	messages := make([]string, 0, partCount)
	for partIndex, offset := 0, 0; offset < len(raw); partIndex, offset = partIndex+1, offset+payloadLimit {
		end := offset + payloadLimit
		if end > len(raw) {
			end = len(raw)
		}
		fragment, err := encodeSocialWordFragment(socialWordFragment{
			groupID:   groupID,
			partIndex: uint16(partIndex),
			partCount: uint16(partCount),
			payload:   raw[offset:end],
		})
		if err != nil {
			return nil, err
		}
		message, err := encodeVKCipherWords(fragment)
		if err != nil {
			return nil, err
		}
		if len(message) > messageLimit {
			return nil, fmt.Errorf("social word fragment size %d exceeds carrier limit %d", len(message), messageLimit)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func socialWordFragmentPayloadLimit(messageLimit int) (int, error) {
	words, _, err := loadVKCipherDictionary()
	if err != nil {
		return 0, err
	}
	maxWordBytes := 0
	for _, word := range words {
		if len(word) > maxWordBytes {
			maxWordBytes = len(word)
		}
	}
	for payloadSize := 1; ; payloadSize++ {
		sourceBytes := 4 + socialWordFragmentHeaderLen + payloadSize
		wordCount := (sourceBytes*8 + vkCipherWordBits - 1) / vkCipherWordBits
		worstCaseBytes := wordCount*maxWordBytes + wordCount - 1
		if worstCaseBytes > messageLimit {
			if payloadSize == 1 {
				return 0, fmt.Errorf("carrier limit %d is too small for social word fragment", messageLimit)
			}
			return payloadSize - 1, nil
		}
	}
}

func encodeSocialWordFragment(fragment socialWordFragment) ([]byte, error) {
	if fragment.partCount < 2 || fragment.partCount > socialWordFragmentMaxParts || fragment.partIndex >= fragment.partCount {
		return nil, fmt.Errorf("invalid social word fragment part %d/%d", fragment.partIndex, fragment.partCount)
	}
	if len(fragment.payload) > int(^uint16(0)) {
		return nil, fmt.Errorf("social word fragment payload too large: %d", len(fragment.payload))
	}
	encoded := make([]byte, socialWordFragmentHeaderLen+len(fragment.payload))
	copy(encoded[:4], socialWordFragmentMagic[:])
	encoded[4] = 1
	copy(encoded[5:17], fragment.groupID[:])
	binary.BigEndian.PutUint16(encoded[17:19], fragment.partIndex)
	binary.BigEndian.PutUint16(encoded[19:21], fragment.partCount)
	binary.BigEndian.PutUint16(encoded[21:23], uint16(len(fragment.payload)))
	copy(encoded[socialWordFragmentHeaderLen:], fragment.payload)
	return encoded, nil
}

func decodeSocialWordFragment(raw []byte) (socialWordFragment, bool, error) {
	if len(raw) < socialWordFragmentHeaderLen || string(raw[:4]) != string(socialWordFragmentMagic[:]) {
		return socialWordFragment{}, false, nil
	}
	if raw[4] != 1 {
		return socialWordFragment{}, true, fmt.Errorf("unsupported social word fragment version %d", raw[4])
	}
	var groupID [12]byte
	copy(groupID[:], raw[5:17])
	partIndex := binary.BigEndian.Uint16(raw[17:19])
	partCount := binary.BigEndian.Uint16(raw[19:21])
	payloadLength := int(binary.BigEndian.Uint16(raw[21:23]))
	if partCount < 2 || partCount > socialWordFragmentMaxParts || partIndex >= partCount {
		return socialWordFragment{}, true, fmt.Errorf("invalid social word fragment part %d/%d", partIndex, partCount)
	}
	if payloadLength != len(raw)-socialWordFragmentHeaderLen {
		return socialWordFragment{}, true, fmt.Errorf("social word fragment payload length %d does not match %d", payloadLength, len(raw)-socialWordFragmentHeaderLen)
	}
	return socialWordFragment{groupID: groupID, partIndex: partIndex, partCount: partCount, payload: raw[socialWordFragmentHeaderLen:]}, true, nil
}

func (a *socialWordFragmentAssembler) accept(scope string, fragment socialWordFragment) ([]byte, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.groups == nil {
		a.groups = make(map[string]socialWordFragmentGroup)
	}
	now := time.Now()
	for key, group := range a.groups {
		if !group.expiresAt.After(now) {
			delete(a.groups, key)
		}
	}
	key := scope + ":" + hex.EncodeToString(fragment.groupID[:])
	group, exists := a.groups[key]
	if !exists {
		if len(a.groups) >= socialWordFragmentMaxGroups {
			return nil, false, fmt.Errorf("too many incomplete social word fragment groups")
		}
		group = socialWordFragmentGroup{partCount: fragment.partCount, parts: make(map[uint16][]byte), expiresAt: now.Add(socialWordFragmentTTL)}
	} else if group.partCount != fragment.partCount {
		return nil, false, fmt.Errorf("social word fragment count changed for group")
	}
	group.parts[fragment.partIndex] = append([]byte(nil), fragment.payload...)
	if len(group.parts) < int(group.partCount) {
		a.groups[key] = group
		return nil, false, nil
	}
	var total int
	for index := uint16(0); index < group.partCount; index++ {
		payload, exists := group.parts[index]
		if !exists {
			a.groups[key] = group
			return nil, false, nil
		}
		total += len(payload)
	}
	assembled := make([]byte, 0, total)
	for index := uint16(0); index < group.partCount; index++ {
		assembled = append(assembled, group.parts[index]...)
	}
	delete(a.groups, key)
	return assembled, true, nil
}

// decodeSocialMessageEnvelopeWithFragments accepts legacy social envelopes,
// one-message word ciphertexts, and word-wrapped fragments. The assembled
// bytes remain untrusted until decodeMailboxEnvelope authenticates them with
// the WhiteTransport session cipher.
func decodeSocialMessageEnvelopeWithFragments(text, scope string, assembler *socialWordFragmentAssembler, cipher ...*fabric.EnvelopeCipher) (fabric.Envelope, bool, error) {
	decoded, candidate, err := decodeVKCipherWords(text)
	if err != nil {
		return fabric.Envelope{}, false, err
	}
	if candidate {
		fragment, fragmented, err := decodeSocialWordFragment(decoded)
		if err != nil {
			return fabric.Envelope{}, false, err
		}
		if fragmented {
			if assembler == nil {
				return fabric.Envelope{}, false, fmt.Errorf("social word fragment needs an assembler")
			}
			assembled, complete, err := assembler.accept(scope, fragment)
			if err != nil || !complete {
				return fabric.Envelope{}, complete, err
			}
			text = string(assembled)
		} else {
			text = string(decoded)
		}
	}
	envelope, err := decodeMailboxEnvelope(text, cipher...)
	if err != nil {
		return fabric.Envelope{}, false, err
	}
	return envelope, true, nil
}

func socialMessageFragmentScope(provider string, values ...string) string {
	return provider + ":" + strings.Join(values, "\x00")
}
