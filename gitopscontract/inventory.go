package gitopscontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	InventoryFilename = ".codefly-render.json"
	SchemaVersion     = 2
)

type Inventory struct {
	SchemaVersion int             `json:"schemaVersion"`
	Module        string          `json:"module"`
	Environment   string          `json:"environment"`
	AppProject    string          `json:"appProject"`
	OwnedPath     string          `json:"ownedPath"`
	ServiceGraph  []Service       `json:"serviceGraph"`
	Files         []InventoryFile `json:"files"`
	Digest        string          `json:"digest"`
}

type Service struct {
	Module  string            `json:"module"`
	Service string            `json:"service"`
	Path    string            `json:"path,omitempty"`
	Managed bool              `json:"managed,omitempty"`
	Output  *KubernetesOutput `json:"output,omitempty"`
}

type KubernetesOutput struct {
	Kind            string                `json:"kind"`
	Profile         string                `json:"profile"`
	ContractVersion string                `json:"contractVersion"`
	Validation      *KubernetesValidation `json:"validation"`
}

type KubernetesValidation struct {
	StaticValidation     string   `json:"staticValidation"`
	ServerSideValidation string   `json:"serverSideValidation"`
	Promotable           bool     `json:"promotable"`
	Violations           []string `json:"violations"`
}

type InventoryFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func Encode(inventory Inventory) ([]byte, error) {
	if inventory.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("CLI render inventory schema is %d, want %d", inventory.SchemaVersion, SchemaVersion)
	}
	data, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode CLI render inventory: %w", err)
	}
	return append(data, '\n'), nil
}

func Decode(data []byte) (Inventory, error) {
	var inventory Inventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return Inventory{}, fmt.Errorf("decode CLI render inventory: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Inventory{}, fmt.Errorf("CLI render inventory contains trailing data")
	}
	canonical, err := Encode(inventory)
	if err != nil {
		return Inventory{}, err
	}
	if !bytes.Equal(data, canonical) {
		return Inventory{}, fmt.Errorf("CLI render inventory is not canonical")
	}
	return inventory, nil
}
