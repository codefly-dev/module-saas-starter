package cataloggen

import (
	"encoding/json"
	"fmt"

	"github.com/codefly-dev/core/composition"

	"accounts/pkg/business"
)

// RenderAPIContractSurfaces publishes the procedures actually served by the
// exported protobuf listeners. Connect registers the entire validated catalog,
// including internal methods; authority serves only its business-owned allowlist.
// Descriptor files remain complete schema inputs, not listener inventories.
func RenderAPIContractSurfaces(catalogDocument, bindingDocument []byte) ([]byte, error) {
	catalog, _, err := decodeRegistrationInputs(catalogDocument, bindingDocument)
	if err != nil {
		return nil, err
	}
	if err := business.ValidateModuleAuthorityProcedures(); err != nil {
		return nil, fmt.Errorf("validate module authority surface: %w", err)
	}
	authorityProcedures := business.ModuleAuthorityProcedures()
	remaining := make(map[string]bool, len(authorityProcedures))
	for _, procedure := range authorityProcedures {
		remaining[procedure] = true
	}
	surfaces := map[string][]composition.APIContractService{}
	for _, service := range catalog.GetServices() {
		connect := composition.APIContractService{
			Name:       service.GetName(),
			FullName:   service.GetFullName(),
			Procedures: service.GetProcedures(),
		}
		surfaces["connect"] = append(surfaces["connect"], connect)
		authority := composition.APIContractService{Name: connect.Name, FullName: connect.FullName}
		for _, procedure := range connect.Procedures {
			if remaining[procedure] {
				authority.Procedures = append(authority.Procedures, procedure)
				delete(remaining, procedure)
			}
		}
		if len(authority.Procedures) > 0 {
			surfaces[business.ModuleAuthorityEndpoint] = append(surfaces[business.ModuleAuthorityEndpoint], authority)
		}
	}
	for _, procedure := range authorityProcedures {
		if remaining[procedure] {
			return nil, fmt.Errorf("module authority procedure %s is absent from the Connect registration catalog", procedure)
		}
	}
	document, err := json.MarshalIndent(surfaces, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode API contract surfaces: %w", err)
	}
	return append(document, '\n'), nil
}
