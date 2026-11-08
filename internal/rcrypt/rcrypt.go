// Package rcrypt implementa la cifratura nel formato di rclone crypt
// (modalità "standard" per i nomi, cifratura anche dei nomi delle cartelle).
// I dati prodotti sono leggibili con un remote rclone di tipo crypt
// configurato con la stessa password e con password2 = salt.
package rcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

const (
	fileMagic       = "RCLONE\x00\x00"
	fileMagicSize   = len(fileMagic)
	fileNonceSize   = 24
	fileHeaderSize  = fileMagicSize + fileNonceSize
	blockHeaderSize = secretbox.Overhead
	blockDataSize   = 64 * 1024
	blockSize       = blockHeaderSize + blockDataSize
	nameBlockSize   = aes.BlockSize
	// MaxSegment è la lunghezza massima di un nome cifrato (limite tipico dei filesystem).
	MaxSegment = 255
)

// defaultSalt è il salt usato da rclone quando password2 è vuota.
var defaultSalt = []byte{0xA8, 0x0D, 0xF4, 0x3A, 0x8F, 0xBD, 0x03, 0x08, 0xA7, 0xCA, 0xB8, 0x3E, 0x58, 0x1F, 0x86, 0xB1}

var (
	ErrBadMagic   = errors.New("rcrypt: intestazione file non valida")
	ErrAuth       = errors.New("rcrypt: autenticazione del blocco fallita (password errata o file corrotto)")
	ErrBadName    = errors.New("rcrypt: nome cifrato non valido")
	ErrNameLength = errors.New("rcrypt: nome troppo lungo una volta cifrato")
)

// Cipher contiene le chiavi derivate dalla password.
type Cipher struct {
	dataKey   [32]byte
	nameKey   [32]byte
	nameTweak [nameBlockSize]byte
	block     cipher.Block
}

// New deriva le chiavi come rclone: scrypt(password, salt, N=16384, r=8, p=1, 80 byte).
func New(password, salt string) (*Cipher, error) {
	if password == "" {
		return nil, errors.New("rcrypt: password vuota")
	}
	c := &Cipher{}
	saltBytes := defaultSalt
	if salt != "" {
		saltBytes = []byte(salt)
	}
	key, err := scrypt.Key([]byte(password), saltBytes, 16384, 8, 1, 32+32+nameBlockSize)
	if err != nil {
		return nil, err
	}
	copy(c.dataKey[:], key[:32])
	copy(c.nameKey[:], key[32:64])
	copy(c.nameTweak[:], key[64:])
	c.block, err = aes.NewCipher(c.nameKey[:])
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ---------- nomi ----------

func encodeName(b []byte) string {
	return strings.ToLower(strings.TrimRight(base32.HexEncoding.EncodeToString(b), "="))
}

func decodeName(s string) ([]byte, error) {
	if strings.HasSuffix(s, "=") {
		return nil, ErrBadName
	}
	pad := ((len(s) + 7) &^ 7) - len(s)
	return base32.HexEncoding.DecodeString(strings.ToUpper(s) + "========"[:pad])
}

func pkcs7Pad(b []byte) []byte {
	n := nameBlockSize - len(b)%nameBlockSize
	return append(append([]byte{}, b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%nameBlockSize != 0 {
		return nil, ErrBadName
	}
	n := int(b[len(b)-1])
	if n == 0 || n > nameBlockSize || n > len(b) {
		return nil, ErrBadName
	}
	for _, x := range b[len(b)-n:] {
		if int(x) != n {
			return nil, ErrBadName
		}
	}
	return b[:len(b)-n], nil
}

// EncryptSegment cifra un singolo elemento di percorso.
func (c *Cipher) EncryptSegment(s string) string {
	if s == "" {
		return ""
	}
	return encodeName(emeTransform(c.block, c.nameTweak[:], pkcs7Pad([]byte(s)), true))
}

// DecryptSegment decifra un singolo elemento di percorso.
func (c *Cipher) DecryptSegment(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	raw, err := decodeName(s)
	if err != nil {
		return "", ErrBadName
	}
	if len(raw) == 0 || len(raw)%nameBlockSize != 0 || len(raw) > 16*8*nameBlockSize {
		return "", ErrBadName
	}
	plain, err := pkcs7Unpad(emeTransform(c.block, c.nameTweak[:], raw, false))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// EncryptPath cifra un percorso relativo con separatore "/".
func (c *Cipher) EncryptPath(p string) (string, error) {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("rcrypt: percorso non valido %q", p)
		}
		if len(s) > 16*8*nameBlockSize-1 {
			return "", ErrNameLength
		}
		e := c.EncryptSegment(s)
		if len(e) > MaxSegment {
			return "", fmt.Errorf("%w: %q", ErrNameLength, s)
		}
		segs[i] = e
	}
	return strings.Join(segs, "/"), nil
}

// DecryptPath decifra un percorso cifrato con separatore "/".
func (c *Cipher) DecryptPath(p string) (string, error) {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		d, err := c.DecryptSegment(s)
		if err != nil {
			return "", err
		}
		segs[i] = d
	}
	return strings.Join(segs, "/"), nil
}

// ---------- contenuto ----------

type nonce [fileNonceSize]byte

func (n *nonce) increment() {
	for i := 0; i < len(n); i++ {
		d := n[i]
		n[i] = d + 1
		if n[i] >= d {
			break
		}
	}
}

// EncryptStream cifra src in dst nel formato file di rclone crypt.
func (c *Cipher) EncryptStream(dst io.Writer, src io.Reader) error {
	var n nonce
	if _, err := io.ReadFull(rand.Reader, n[:]); err != nil {
		return err
	}
	if _, err := dst.Write(append([]byte(fileMagic), n[:]...)); err != nil {
		return err
	}
	buf := make([]byte, blockDataSize)
	out := make([]byte, 0, blockSize)
	for {
		k, err := io.ReadFull(src, buf)
		if k > 0 {
			nn := [24]byte(n)
			out = secretbox.Seal(out[:0], buf[:k], &nn, &c.dataKey)
			if _, werr := dst.Write(out); werr != nil {
				return werr
			}
			n.increment()
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// DecryptStream decifra src (formato rclone crypt) in dst.
func (c *Cipher) DecryptStream(dst io.Writer, src io.Reader) error {
	hdr := make([]byte, fileHeaderSize)
	if _, err := io.ReadFull(src, hdr); err != nil {
		return ErrBadMagic
	}
	if string(hdr[:fileMagicSize]) != fileMagic {
		return ErrBadMagic
	}
	var n nonce
	copy(n[:], hdr[fileMagicSize:])
	buf := make([]byte, blockSize)
	out := make([]byte, 0, blockDataSize)
	for {
		k, err := io.ReadFull(src, buf)
		if k > 0 {
			if k <= blockHeaderSize {
				return ErrAuth
			}
			nn := [24]byte(n)
			var ok bool
			out, ok = secretbox.Open(out[:0], buf[:k], &nn, &c.dataKey)
			if !ok {
				return ErrAuth
			}
			if _, werr := dst.Write(out); werr != nil {
				return werr
			}
			n.increment()
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Seal cifra un piccolo buffer in memoria.
func (c *Cipher) Seal(b []byte) ([]byte, error) {
	var out bytes.Buffer
	err := c.EncryptStream(&out, bytes.NewReader(b))
	return out.Bytes(), err
}

// Open decifra un piccolo buffer in memoria.
func (c *Cipher) Open(b []byte) ([]byte, error) {
	var out bytes.Buffer
	err := c.DecryptStream(&out, bytes.NewReader(b))
	return out.Bytes(), err
}

// CheckPlain è il testo cifrato in vault-meta.json per verificare la password.
const CheckPlain = "encluca-password-ok"

// DecryptedSize restituisce la dimensione in chiaro di un file cifrato di n byte
// (-1 se n non è una dimensione valida). È l'inverso di EncryptedSize.
func DecryptedSize(n int64) int64 {
	n -= int64(fileHeaderSize)
	if n < 0 {
		return -1
	}
	blocks := n / blockSize
	rest := n % blockSize
	size := blocks * blockDataSize
	if rest > 0 {
		if rest <= int64(blockHeaderSize) {
			return -1
		}
		size += rest - int64(blockHeaderSize)
	}
	return size
}

// EncryptedSize restituisce la dimensione cifrata di un file in chiaro di n byte.
func EncryptedSize(n int64) int64 {
	blocks := n / blockDataSize
	rest := n % blockDataSize
	size := int64(fileHeaderSize) + blocks*blockSize
	if rest > 0 {
		size += int64(blockHeaderSize) + rest
	}
	return size
}

// ---------- EME (Halevi-Rogaway), come github.com/rfjakob/eme ----------

func multByTwo(out, in []byte) {
	tmp := make([]byte, 16)
	tmp[0] = 2 * in[0]
	if in[15] >= 128 {
		tmp[0] ^= 135
	}
	for j := 1; j < 16; j++ {
		tmp[j] = 2 * in[j]
		if in[j-1] >= 128 {
			tmp[j]++
		}
	}
	copy(out, tmp)
}

func xorBlocks(out, a, b []byte) {
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
}

func aesT(dst, src []byte, enc bool, bc cipher.Block) {
	if enc {
		bc.Encrypt(dst, src)
	} else {
		bc.Decrypt(dst, src)
	}
}

func emeTransform(bc cipher.Block, tweak, P []byte, enc bool) []byte {
	m := len(P) / 16
	if m == 0 || m > 16*8 || len(P)%16 != 0 || len(tweak) != 16 {
		panic("eme: dimensioni non valide")
	}
	C := make([]byte, len(P))
	// tabella L
	L := make([][]byte, m)
	Li := make([]byte, 16)
	bc.Encrypt(Li, make([]byte, 16))
	for i := 0; i < m; i++ {
		multByTwo(Li, Li)
		L[i] = append([]byte{}, Li...)
	}
	PPj := make([]byte, 16)
	for j := 0; j < m; j++ {
		xorBlocks(PPj, P[j*16:(j+1)*16], L[j])
		aesT(C[j*16:(j+1)*16], PPj, enc, bc)
	}
	MP := make([]byte, 16)
	xorBlocks(MP, C[0:16], tweak)
	for j := 1; j < m; j++ {
		xorBlocks(MP, MP, C[j*16:(j+1)*16])
	}
	MC := make([]byte, 16)
	aesT(MC, MP, enc, bc)
	M := make([]byte, 16)
	xorBlocks(M, MP, MC)
	CCCj := make([]byte, 16)
	for j := 1; j < m; j++ {
		multByTwo(M, M)
		xorBlocks(CCCj, C[j*16:(j+1)*16], M)
		copy(C[j*16:(j+1)*16], CCCj)
	}
	CCC1 := make([]byte, 16)
	xorBlocks(CCC1, MC, tweak)
	for j := 1; j < m; j++ {
		xorBlocks(CCC1, CCC1, C[j*16:(j+1)*16])
	}
	copy(C[0:16], CCC1)
	for j := 0; j < m; j++ {
		aesT(C[j*16:(j+1)*16], C[j*16:(j+1)*16], enc, bc)
		xorBlocks(C[j*16:(j+1)*16], C[j*16:(j+1)*16], L[j])
	}
	return C
}
