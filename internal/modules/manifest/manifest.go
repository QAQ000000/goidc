// Package manifest exposes only public compiled-module metadata. It does not
// load environment configuration, contact a database or instantiate adapters.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/modules/all"
)

const Format = "goidc-compiled-modules-v1"

type Module struct {
	Category         channel.Category `json:"category"`
	ID               string           `json:"id"`
	Version          string           `json:"version"`
	SchemaVersion    int              `json:"schema_version"`
	ContractVersion  int              `json:"contract_version"`
	InstanceMode     string           `json:"instance_mode"`
	FactoryAvailable bool             `json:"factory_available"`
	DescriptorSHA256 string           `json:"descriptor_sha256"`
}

type Manifest struct {
	Format          string   `json:"format"`
	Profile         string   `json:"profile"`
	Binary          string   `json:"binary"`
	ContractVersion int      `json:"contract_version"`
	Modules         []Module `json:"modules"`
}

func Current(binary string) Manifest {
	out := Manifest{Format: Format, Profile: all.Profile, Binary: binary, ContractVersion: channel.ContractVersion, Modules: make([]Module, 0)}
	for _, descriptor := range channel.List("") {
		raw, err := json.Marshal(descriptor)
		if err != nil {
			panic("compiled module descriptor cannot be encoded")
		}
		digest := sha256.Sum256(raw)
		out.Modules = append(out.Modules, Module{
			Category: descriptor.Category, ID: descriptor.ID, Version: descriptor.Version,
			SchemaVersion: descriptor.SchemaVersion, ContractVersion: descriptor.ContractVersion,
			InstanceMode: descriptor.InstanceMode, FactoryAvailable: channel.HasFactory(descriptor.Category, descriptor.ID),
			DescriptorSHA256: hex.EncodeToString(digest[:]),
		})
	}
	return out
}

// WriteCLI handles introspection before config.Load and database initialization.
// Mixing it with other flags fails instead of starting a process unexpectedly.
// False means normal startup should run.
func WriteCLI(args []string, writer io.Writer, binary string) (bool, error) {
	if !slices.Contains(args, "--module-manifest") {
		return false, nil
	}
	if len(args) != 1 || (binary != "api" && binary != "worker") {
		return true, errors.New("usage: api|worker --module-manifest")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return true, encoder.Encode(Current(binary))
}
