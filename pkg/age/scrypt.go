// Copyright 2019 Google LLC
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

package age

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"

	"github.com/aead/age/pkg/format"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const scryptLabel = "age-encryption.org/v1/scrypt"

// ScryptRecipient is a password-based recipient.
//
// If a ScryptRecipient is used, it must be the only recipient for the file: it
// can't be mixed with other recipient types and can't be used multiple times
// for the same file.
//
// Its use is not recommended for automated systems, which should prefer
// X25519Recipient.
type ScryptRecipient struct {
	password   []byte
	workFactor int
}

var _ Recipient = &ScryptRecipient{}

func (*ScryptRecipient) Type() string { return "scrypt" }

// NewScryptRecipient returns a new ScryptRecipient with the provided password.
func NewScryptRecipient(password string) (*ScryptRecipient, error) {
	if len(password) == 0 {
		return nil, errors.New("passphrase can't be empty")
	}
	r := &ScryptRecipient{
		password: []byte(password),
		// TODO: automatically scale this to 1s (with a min) in the CLI.
		workFactor: 18, // 1s on a modern machine
	}
	return r, nil
}

// SetWorkFactor sets the scrypt work factor to 2^logN.
// It must be called before Wrap.
//
// If SetWorkFactor is not called, a reasonable default is used.
func (r *ScryptRecipient) SetWorkFactor(logN int) {
	if logN > 30 || logN < 1 {
		panic("age: SetWorkFactor called with illegal value")
	}
	r.workFactor = logN
}

func (r *ScryptRecipient) Wrap(fileKey []byte) (*format.Recipient, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, err
	}

	logN := r.workFactor
	l := &format.Recipient{
		Type: "scrypt",
		Args: []string{format.EncodeToString(salt), strconv.Itoa(logN)},
	}

	salt = append([]byte(scryptLabel), salt...)
	k, err := scrypt.Key(r.password, salt, 1<<logN, 8, 1, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("failed to generate scrypt hash: %v", err)
	}

	wrappedKey, err := aeadEncrypt(k, fileKey)
	if err != nil {
		return nil, err
	}
	l.Body = wrappedKey

	return l, nil
}

// ScryptIdentity is a password-based identity.
type ScryptIdentity struct {
	password      []byte
	maxWorkFactor int
}

var _ Identity = &ScryptIdentity{}

func (*ScryptIdentity) Type() string { return "scrypt" }

// NewScryptIdentity returns a new ScryptIdentity with the provided password.
func NewScryptIdentity(password string) (*ScryptIdentity, error) {
	if len(password) == 0 {
		return nil, errors.New("passphrase can't be empty")
	}
	i := &ScryptIdentity{
		password:      []byte(password),
		maxWorkFactor: 22, // 15s on a modern machine
	}
	return i, nil
}

// SetMaxWorkFactor sets the maximum accepted scrypt work factor to 2^logN.
// It must be called before Unwrap.
//
// This caps the amount of work that Decrypt might have to do to process
// received files. If SetMaxWorkFactor is not called, a fairly high default is
// used, which might not be suitable for systems processing untrusted files.
func (i *ScryptIdentity) SetMaxWorkFactor(logN int) {
	if logN > 30 || logN < 1 {
		panic("age: SetMaxWorkFactor called with illegal value")
	}
	i.maxWorkFactor = logN
}

func (i *ScryptIdentity) Unwrap(block *format.Recipient) ([]byte, error) {
	if block.Type != "scrypt" {
		return nil, ErrIncorrectIdentity
	}
	if len(block.Args) != 2 {
		return nil, errors.New("invalid scrypt recipient block")
	}
	salt, err := format.DecodeString(block.Args[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse scrypt salt: %v", err)
	}
	if len(salt) != 16 {
		return nil, errors.New("invalid scrypt recipient block")
	}
	logN, err := strconv.Atoi(block.Args[1])
	if err != nil {
		return nil, fmt.Errorf("failed to parse scrypt work factor: %v", err)
	}
	if logN > i.maxWorkFactor {
		return nil, fmt.Errorf("scrypt work factor too large: %v", logN)
	}
	if logN <= 0 {
		return nil, fmt.Errorf("invalid scrypt work factor: %v", logN)
	}

	salt = append([]byte(scryptLabel), salt...)
	k, err := scrypt.Key(i.password, salt, 1<<logN, 8, 1, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("failed to generate scrypt hash: %v", err)
	}

	fileKey, err := aeadDecrypt(k, block.Body)
	if err != nil {
		return nil, ErrIncorrectIdentity
	}
	return fileKey, nil
}