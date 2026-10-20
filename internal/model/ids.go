// Package model holds the value types shared across the vault packages.
package model

import (
	"encoding/hex"
	"fmt"
	"io"
)

type ObjectID string

const RootID ObjectID = "root"

type DeviceID string

type Kind uint8

const (
	KindFile Kind = iota + 1
	KindFolder
)

type LocalState uint8

const (
	CloudOnly LocalState = iota + 1
	Partial
	Local
	Dirty
	Corrupt
)

type Ino uint64

func NewObjectID(random io.Reader) (ObjectID, error) {
	id, err := randomHex(random, 16)
	return ObjectID(id), err
}

func NewDeviceID(random io.Reader) (DeviceID, error) {
	id, err := randomHex(random, 8)
	return DeviceID("d-" + id), err
}

func NewRandomID(random io.Reader) (string, error) {
	return randomHex(random, 16)
}

func randomHex(random io.Reader, size int) (string, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("read random id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
