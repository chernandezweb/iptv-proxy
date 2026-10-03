package xtreamcodes

// Package base64 provides a byte slice type that marshals into json as
// a raw (no padding) base64url value.
// Originally from https://github.com/manifoldco/go-base64

import (
	"encoding/base64"
	"errors"
	"reflect"
)

// Base64Value is a base64url encoded json object,
type Base64Value []byte

// New returns a pointer to a Base64Value, cast from the given byte slice.
// This is a convenience function, handling the address creation that a direct
// cast would not allow.
func New(b []byte) *Base64Value {
	v := Base64Value(b)
	return &v
}

// NewFromString returns a Base64Value containing the decoded data in encoded.
func NewFromString(encoded string) (*Base64Value, error) {
	out, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	return New(out), nil
}

// MarshalJSON returns the ba64url encoding of bv for JSON representation.
func (bv *Base64Value) MarshalJSON() ([]byte, error) {
	return []byte("\"" + base64.RawURLEncoding.EncodeToString(*bv) + "\""), nil
}

func (bv *Base64Value) String() string {
	return base64.RawURLEncoding.EncodeToString(*bv)
}

// UnmarshalJSON sets bv to the bytes represented in base64 (supporting Std, URL, and raw string formats).
func (bv *Base64Value) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != byte('"') || b[len(b)-1] != byte('"') {
		return errors.New("value is not a string")
	}

	raw := b[1 : len(b)-1]
	if len(raw) == 0 {
		return nil
	}

	// Try standard base64 (with padding)
	out := make([]byte, base64.StdEncoding.DecodedLen(len(raw)))
	n, err := base64.StdEncoding.Decode(out, raw)
	if err == nil {
		v := reflect.ValueOf(bv).Elem()
		v.SetBytes(out[:n])
		return nil
	}

	// Try URL-safe raw base64
	out = make([]byte, base64.RawURLEncoding.DecodedLen(len(raw)))
	n, err = base64.RawURLEncoding.Decode(out, raw)
	if err == nil {
		v := reflect.ValueOf(bv).Elem()
		v.SetBytes(out[:n])
		return nil
	}

	// Try standard raw base64 (no padding)
	out = make([]byte, base64.RawStdEncoding.DecodedLen(len(raw)))
	n, err = base64.RawStdEncoding.Decode(out, raw)
	if err == nil {
		v := reflect.ValueOf(bv).Elem()
		v.SetBytes(out[:n])
		return nil
	}

	// Fallback to literal plain text string if upstream provided unencoded string
	v := reflect.ValueOf(bv).Elem()
	v.SetBytes(raw)
	return nil
}
