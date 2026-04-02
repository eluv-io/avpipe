package mp4e

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/eluv-io/errors-go"
)

// CryptScheme identifies the encryption scheme for DecryptFile.
type CryptScheme string

const (
	CryptCENC   CryptScheme = "cenc"
	CryptCBCS   CryptScheme = "cbcs"
	CryptAES128 CryptScheme = "aes-128"
)

// DecryptFile decrypts an encrypted fMP4 file and writes the cleartext to dst.
// It dispatches to the appropriate decryption method based on the scheme:
//   - cenc/cbcs: mp4ff ISO BMFF Common Encryption (sample-level).
//     For DASH media segments (.m4s) that lack a moov box, pass the init
//     segment path as initSeg so the decryption metadata can be extracted.
//   - aes-128:   AES-CBC whole-file decryption (HLS-style, PKCS7 padded)
func DecryptFile(src, dst string, scheme CryptScheme, key, iv []byte, initSeg ...string) error {
	switch scheme {
	case CryptCENC, CryptCBCS:
		init := ""
		if len(initSeg) > 0 {
			init = initSeg[0]
		}
		return decryptFileCenc(src, dst, key, init)
	case CryptAES128:
		return decryptFileAES128(src, dst, key, iv)
	default:
		return fmt.Errorf("unsupported encryption scheme: %q", scheme)
	}
}

// decryptFileCenc decrypts a CENC or CBCS encrypted fMP4 file using mp4ff.
// If initSeg is provided, it is parsed to extract the DecryptInfo (needed for
// DASH media segments that don't contain a moov box). If empty, the source
// file must be self-contained (init + segments in one file).
func decryptFileCenc(src, dst string, key []byte, initSeg string) error {
	e := errors.Template("mp4e.decryptFileCenc", errors.K.Invalid.Default())

	data, err := os.ReadFile(src)
	if err != nil {
		return e(err, "reason", "read source")
	}

	file, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		return e(err, "reason", "decode mp4")
	}

	// Get DecryptInfo from the init segment
	var di mp4.DecryptInfo
	if initSeg != "" {
		// Parse external init segment for decryption metadata
		initData, rErr := os.ReadFile(initSeg)
		if rErr != nil {
			return e(rErr, "reason", "read init segment")
		}
		initFile, dErr := mp4.DecodeFile(bytes.NewReader(initData))
		if dErr != nil {
			return e(dErr, "reason", "decode init segment")
		}
		if initFile.Init == nil {
			return e("reason", "no init segment in init file")
		}
		di, err = mp4.DecryptInit(initFile.Init)
		if err != nil {
			return e(err, "reason", "decrypt init")
		}
	} else if file.Init != nil {
		di, err = mp4.DecryptInit(file.Init)
		if err != nil {
			return e(err, "reason", "decrypt init")
		}
	} else {
		return e("reason", "no init segment in file and no external init segment provided")
	}

	for i, seg := range file.Segments {
		if err = mp4.DecryptSegment(seg, di, key); err != nil {
			return e(err, "reason", fmt.Sprintf("decrypt segment %d", i))
		}
	}

	var buf bytes.Buffer
	if err = file.Encode(&buf); err != nil {
		return e(err, "reason", "encode decrypted mp4")
	}

	if err = os.WriteFile(dst, buf.Bytes(), 0644); err != nil {
		return e(err, "reason", "write destination")
	}

	return nil
}

// decryptFileAES128 decrypts an AES-128-CBC encrypted file (HLS-style).
// The ciphertext is expected to be PKCS7-padded.
func decryptFileAES128(src, dst string, key, iv []byte) error {
	e := errors.Template("mp4e.decryptFileAES128", errors.K.Invalid.Default())

	ciphertext, err := os.ReadFile(src)
	if err != nil {
		return e(err, "reason", "read source")
	}

	if len(ciphertext) == 0 {
		return e("reason", "empty ciphertext")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return e("reason", fmt.Sprintf("ciphertext length %d is not a multiple of AES block size", len(ciphertext)))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return e(err, "reason", "create AES cipher")
	}

	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	plaintext, err = pkcs7Unpad(plaintext, aes.BlockSize)
	if err != nil {
		return e(err, "reason", "PKCS7 unpad")
	}

	if err = os.WriteFile(dst, plaintext, 0644); err != nil {
		return e(err, "reason", "write destination")
	}

	return nil
}

// pkcs7Unpad removes PKCS7 padding from data.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, fmt.Errorf("invalid PKCS7 padding: %d", padLen)
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, fmt.Errorf("invalid PKCS7 padding byte")
		}
	}
	return data[:len(data)-padLen], nil
}

// DecryptReader decrypts an encrypted fMP4 from a reader and returns the
// cleartext as a reader. Same dispatch logic as DecryptFile.
func DecryptReader(r io.Reader, scheme CryptScheme, key, iv []byte) (io.Reader, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}

	switch scheme {
	case CryptCENC, CryptCBCS:
		return decryptReaderCenc(data, key)
	case CryptAES128:
		return decryptReaderAES128(data, key, iv)
	default:
		return nil, fmt.Errorf("unsupported encryption scheme: %q", scheme)
	}
}

func decryptReaderCenc(data, key []byte) (io.Reader, error) {
	file, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode mp4: %w", err)
	}
	if file.Init == nil {
		return nil, fmt.Errorf("no init segment in file")
	}
	di, err := mp4.DecryptInit(file.Init)
	if err != nil {
		return nil, fmt.Errorf("decrypt init: %w", err)
	}
	for i, seg := range file.Segments {
		if err = mp4.DecryptSegment(seg, di, key); err != nil {
			return nil, fmt.Errorf("decrypt segment %d: %w", i, err)
		}
	}
	var buf bytes.Buffer
	if err = file.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode decrypted mp4: %w", err)
	}
	return &buf, nil
}

func decryptReaderAES128(ciphertext, key, iv []byte) (io.Reader, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("empty ciphertext")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of AES block size", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	plaintext, err = pkcs7Unpad(plaintext, aes.BlockSize)
	if err != nil {
		return nil, fmt.Errorf("PKCS7 unpad: %w", err)
	}
	return bytes.NewReader(plaintext), nil
}
