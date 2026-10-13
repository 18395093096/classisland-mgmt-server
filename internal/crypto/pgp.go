// Package crypto 处理服务端 PGP 密钥对与握手挑战令牌的解密。
//
// 客户端（ClassIsland）在注册时会拿到服务器公钥，并在握手时用该公钥加密一段随机令牌；
// 服务器必须能解密并原样返回，客户端才会信任这台服务器并建立命令流。
package crypto

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"
	"golang.org/x/crypto/openpgp/packet"
)

// KeyPair 服务端 PGP 密钥对
type KeyPair struct {
	entity     *openpgp.Entity
	PublicKey  string // ASCII Armor 公钥，下发给客户端
	KeyID      uint64
	Fingerprint string
}

// LoadOrCreate 载入已有密钥，不存在时生成新的 RSA 2048 密钥对并落盘。
func LoadOrCreate(dir string) (*KeyPair, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	privPath := filepath.Join(dir, "server-private.asc")
	pubPath := filepath.Join(dir, "server-public.asc")

	if b, err := os.ReadFile(privPath); err == nil {
		el, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(b))
		if err == nil && len(el) > 0 {
			return fromEntity(el[0], pubPath)
		}
	}
	// 生成新密钥。
	// 默认偏好里包含 RIPEMD160，而 .NET（BouncyCastle）与部分 OpenPGP 实现并不支持它，
	// 这里通过 Config 显式指定只用 SHA-256 + AES-256，避免客户端加密时选到不支持的算法。
	entity, err := openpgp.NewEntity("ClassIsland Management Server", "服务器密钥", "no-reply@classisland.local",
		&packet.Config{
			DefaultHash:   crypto.SHA256,
			DefaultCipher: packet.CipherAES256,
		})
	if err != nil {
		return nil, err
	}
	var privBuf bytes.Buffer
	w, err := armor.Encode(&privBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return nil, err
	}
	if err := entity.SerializePrivate(w, nil); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(privPath, privBuf.Bytes(), 0o600); err != nil {
		return nil, err
	}
	return fromEntity(entity, pubPath)
}

func fromEntity(entity *openpgp.Entity, pubPath string) (*KeyPair, error) {
	var pubBuf bytes.Buffer
	w, err := armor.Encode(&pubBuf, openpgp.PublicKeyType, nil)
	if err != nil {
		return nil, err
	}
	if err := entity.Serialize(w); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pubPath, pubBuf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return &KeyPair{
		entity:      entity,
		PublicKey:   pubBuf.String(),
		KeyID:       entity.PrimaryKey.KeyId,
		Fingerprint: strings.ToUpper(hexString(entity.PrimaryKey.Fingerprint[:])),
	}, nil
}

// Decrypt 解密客户端用服务器公钥加密的内容，返回明文字符串。
func (k *KeyPair) Decrypt(armored string) (string, error) {
	block, err := armor.Decode(strings.NewReader(armored))
	if err != nil {
		// 客户端可能直接发送未 armored 的二进制数据
		md, err2 := openpgp.ReadMessage(bytes.NewReader([]byte(armored)), openpgp.EntityList{k.entity}, nil, nil)
		if err2 != nil {
			return "", err
		}
		b, err3 := io.ReadAll(md.UnverifiedBody)
		return string(b), err3
	}
	md, err := openpgp.ReadMessage(block.Body, openpgp.EntityList{k.entity}, nil, nil)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(md.UnverifiedBody)
	return string(b), err
}

// RandomBase64 生成随机令牌（服务端主动发起校验时使用）
func RandomBase64(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64Encode(buf)
}
