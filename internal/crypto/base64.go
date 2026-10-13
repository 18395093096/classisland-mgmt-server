package crypto

import "encoding/base64"

func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0xf]
	}
	return string(out)
}
