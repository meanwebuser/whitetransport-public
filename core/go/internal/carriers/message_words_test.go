package carriers

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestVKCipherWordsUseTheExactV4DictionaryAndWireVector(t *testing.T) {
	digest := sha256.Sum256([]byte(vkcipherWordsV4))
	const expectedDigest = "d6ce1bca2d8715a390842773d65a88b643e9f95bec6e9e4eda7b81c0aa88a2a4"
	if got := fmt.Sprintf("%x", digest); got != expectedDigest {
		t.Fatalf("vkcipher v4 dictionary digest = %s, want %s", got, expectedDigest)
	}

	const plaintext = "wtenc1.AQID"
	const expectedWords = "не не пирог табак диагноз паралич бензин любовь беспорядок подчиняться"
	encoded, err := encodeVKCipherWords([]byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	if encoded != expectedWords {
		t.Fatalf("vkcipher word vector = %q, want %q", encoded, expectedWords)
	}
	decoded, candidate, err := decodeVKCipherWords(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate || string(decoded) != plaintext {
		t.Fatalf("decoded candidate=%t plaintext=%q, want %q", candidate, decoded, plaintext)
	}
}

func TestVKCipherWordsRejectTamperedEncryptedEnvelope(t *testing.T) {
	key := fabric.DeriveBootstrapKey("word-transport-tamper-fixture")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeSocialMessageEnvelope(fabric.NewEnvelope("word-tamper", fabric.TrafficControl, "control.frame", []byte("never visible")), cipher)
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(encoded)
	if len(words) < 2 {
		t.Fatalf("need multiple words to tamper, got %q", encoded)
	}
	_, indexes, err := loadVKCipherDictionary()
	if err != nil {
		t.Fatal(err)
	}
	last := indexes[words[len(words)-1]]
	words[len(words)-1] = loadNextVKCipherWord(t, int(last))
	if _, err := decodeSocialMessageEnvelope(strings.Join(words, " "), cipher); err == nil {
		t.Fatal("expected authenticated envelope rejection after word tampering")
	}
}

func TestOnlySocialMessageEnvelopesUseVKCipherWords(t *testing.T) {
	key := fabric.DeriveBootstrapKey("social-message-scope-fixture")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("scope", fabric.TrafficControl, "control.frame", []byte("social-only"))
	regular, err := encodeMailboxEnvelope(envelope, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(regular, encryptedMessagePrefix) {
		t.Fatalf("non-social mailbox transport changed to %q", regular)
	}
	if _, candidate, err := decodeVKCipherWords(regular); err != nil || candidate {
		t.Fatalf("non-social mailbox text became a word candidate: candidate=%t err=%v", candidate, err)
	}

	social, err := encodeSocialMessageEnvelope(envelope, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if _, candidate, err := decodeVKCipherWords(social); err != nil || !candidate {
		t.Fatalf("social mailbox text is not a VKCipher word candidate: candidate=%t err=%v", candidate, err)
	}
}

func loadNextVKCipherWord(t *testing.T, index int) string {
	t.Helper()
	words, _, err := loadVKCipherDictionary()
	if err != nil {
		t.Fatal(err)
	}
	return words[(index+1)%len(words)]
}

func requireWordCiphertext(t *testing.T, text, plaintext string) {
	t.Helper()
	if strings.Contains(text, plaintext) {
		t.Fatalf("provider-visible text leaked plaintext %q", plaintext)
	}
	if strings.HasPrefix(text, mailboxMessagePrefix) || strings.HasPrefix(text, encryptedMessagePrefix) {
		t.Fatalf("provider-visible text must not expose a WhiteTransport prefix: %q", text)
	}
	words := strings.Fields(text)
	if len(words) < 2 {
		t.Fatalf("expected word ciphertext, got %q", text)
	}
	for _, word := range words {
		if !isRussianWord(word) {
			t.Fatalf("expected Russian word ciphertext, got word %q in %q", word, text)
		}
	}
}

func isRussianWord(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if !unicode.Is(unicode.Cyrillic, r) {
			return false
		}
	}
	return true
}
