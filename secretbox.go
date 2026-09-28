package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// secretBox는 DB에 넣기 전에 값을 잠그고, 읽어 올 때 연다.
//
// 지키려는 것은 DB가 통째로 새어 나가는 경우다. Neon 덤프나 백업이 남의 손에
// 가도 제휴 키를 바로 쓰지 못하게 한다. 서버 자체가 털리면 열쇠도 같이 털리므로
// 그때까지 막아 주지는 못한다.
type secretBox struct{ aead cipher.AEAD }

// newSecretBox는 아무 길이의 문자열에서 열쇠를 만든다. 사람이 고른 문장이든
// Render가 만들어 준 값이든 그대로 받아 쓰려고 해시를 한 번 거친다.
func newSecretBox(secret string) (*secretBox, error) {
	if secret == "" {
		return nil, errors.New("암호화 열쇠가 비어 있다")
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

// seal은 잠근 값을 text 컬럼에 담을 수 있게 base64로 돌려준다.
// 같은 값을 두 번 잠가도 매번 다르게 나온다. nonce가 매번 새로 나오기 때문이다.
func (b *secretBox) seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// open은 잠근 값을 되돌린다. 열쇠가 바뀌었거나 값이 망가졌으면 실패한다.
func (b *secretBox) open(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("잠긴 값을 읽지 못했다: %w", err)
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("잠긴 값이 너무 짧다")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		// 열쇠를 바꿨을 때 여기로 온다. 무엇이 틀렸는지는 알려 주지 않는다.
		return "", errors.New("잠긴 값을 열지 못했다. 암호화 열쇠가 바뀌었을 수 있다")
	}
	return string(plain), nil
}
