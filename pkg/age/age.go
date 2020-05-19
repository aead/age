// Copyright 2019 Google LLC
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

// Package age implements file encryption according to age-encryption.org/v1.
package age

import (
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/aead/age/pkg/format"
	"github.com/aead/age/pkg/stream"
)

// An Identity is a private key or other value that can decrypt an opaque file
// key from a recipient stanza.
//
// Unwrap must return ErrIncorrectIdentity for recipient blocks that don't match
// the identity, any other error might be considered fatal.
type Identity interface {
	Type() string
	Unwrap(block *format.Recipient) (fileKey []byte, err error)
}

// IdentityMatcher can be optionally implemented by an Identity that can
// communicate whether it can decrypt a recipient stanza without decrypting it.
//
// If an Identity implements IdentityMatcher, its Unwrap method will only be
// invoked on blocks for which Match returned nil. Match must return
// ErrIncorrectIdentity for recipient blocks that don't match the identity, any
// other error might be considered fatal.
type IdentityMatcher interface {
	Identity
	Match(block *format.Recipient) error
}

var ErrIncorrectIdentity = errors.New("incorrect identity for recipient block")

// A Recipient is a public key or other value that can encrypt an opaque file
// key to a recipient stanza.
type Recipient interface {
	Type() string
	Wrap(fileKey []byte) (*format.Recipient, error)
}

// Encrypt returns a WriteCloser. Writes to the returned value are encrypted and
// written to dst as an age file. Every recipient will be able to decrypt the file.
//
// The caller must call Close on the returned value when done for the last chunk
// to be encrypted and flushed to dst.
func Encrypt(dst io.Writer, recipients ...Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no recipients specified")
	}

	fileKey := make([]byte, 16)
	if _, err := rand.Read(fileKey); err != nil {
		return nil, err
	}

	hdr := &format.Header{}
	for i, r := range recipients {
		if r.Type() == "scrypt" && len(recipients) != 1 {
			return nil, errors.New("an scrypt recipient must be the only one")
		}

		block, err := r.Wrap(fileKey)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap key for recipient #%d: %v", i, err)
		}
		hdr.Recipients = append(hdr.Recipients, block)
	}
	if mac, err := headerMAC(fileKey, hdr); err != nil {
		return nil, fmt.Errorf("failed to compute header MAC: %v", err)
	} else {
		hdr.MAC = mac
	}
	if err := hdr.Marshal(dst); err != nil {
		return nil, fmt.Errorf("failed to write header: %v", err)
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	if _, err := dst.Write(nonce); err != nil {
		return nil, fmt.Errorf("failed to write nonce: %v", err)
	}

	return stream.NewWriter(streamKey(fileKey, nonce), dst)
}

// Decrypt returns a Reader reading the decrypted plaintext of the age file read
// from src. All identities will be tried until one successfully decrypts the file.
func Decrypt(src io.Reader, identities ...Identity) (io.Reader, error) {
	if len(identities) == 0 {
		return nil, errors.New("no identities specified")
	}

	hdr, payload, err := format.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("failed to read header: %v", err)
	}
	if len(hdr.Recipients) > 20 {
		return nil, errors.New("too many recipients")
	}

	var fileKey []byte
RecipientsLoop:
	for _, r := range hdr.Recipients {
		if r.Type == "scrypt" && len(hdr.Recipients) != 1 {
			return nil, errors.New("an scrypt recipient must be the only one")
		}
		for _, i := range identities {
			if i.Type() != r.Type {
				continue
			}

			if i, ok := i.(IdentityMatcher); ok {
				err := i.Match(r)
				if err != nil {
					if err == ErrIncorrectIdentity {
						continue
					}
					return nil, err
				}
			}

			fileKey, err = i.Unwrap(r)
			if err != nil {
				if err == ErrIncorrectIdentity {
					// TODO: we should collect these errors and return them as an
					// []error type with an Error method. That will require turning
					// ErrIncorrectIdentity into an interface or wrapper error.
					continue
				}
				return nil, err
			}

			break RecipientsLoop
		}
	}
	if fileKey == nil {
		return nil, errors.New("no identity matched a recipient")
	}

	if mac, err := headerMAC(fileKey, hdr); err != nil {
		return nil, fmt.Errorf("failed to compute header MAC: %v", err)
	} else if !hmac.Equal(mac, hdr.MAC) {
		return nil, errors.New("bad header MAC")
	}

	nonce := make([]byte, 16)
	if _, err := io.ReadFull(payload, nonce); err != nil {
		return nil, fmt.Errorf("failed to read nonce: %v", err)
	}

	return stream.NewReader(streamKey(fileKey, nonce), payload)
}
