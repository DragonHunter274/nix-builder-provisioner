package nixproto

import (
	"encoding/json" // added
	"fmt"
	"log"
	"strings" // added
	"testing"
)

func TestMakeDrvOutputID(t *testing.T) {
	// Example from a real nix store path
	// path: /nix/store/z58sjjsad54x6q59p87y883196901p6g-hello-2.12.1.drv
	// hash part: z58sjjsad54x6q59p87y883196901p6g
	// output: out

	drvPath := "/nix/store/z58sjjsad54x6q59p87y883196901p6g-hello-2.12.1.drv"
	outputName := "out"

	id, err := MakeDrvOutputID(drvPath, outputName)
	if err != nil {
		t.Fatalf("MakeDrvOutputID failed: %v", err)
	}

	// We don't have the exact expected sha256 here without running nix, but we can verify the format
	// and that it produces a valid hex string after the prefix
	t.Logf("Generated ID: %s", id)

	if len(id) < 64 { // sha256:<64 chars>!<name>
		t.Errorf("ID too short: %s", id)
	}
}

func TestPopulateBuiltOutputs(t *testing.T) {
	// Simulate the scenario where we have a derivation and an AlreadyValid result
	// drvPath := "/nix/store/z58sjjsad54x6q59p87y883196901p6g-hello-2.12.1.drv"
	drv := &BasicDerivation{
		Outputs: map[string]DerivationOutput{
			"out": {
				Path:     "/nix/store/d6r4q1j4y1j4y1j4y1j4y1j4y1j4y1j4-hello-2.12.1",
				HashAlgo: "sha256",
				Hash:     "",
			},
		},
	}

	result := &BuildResult{
		Status:       BuildResultAlreadyValid,
		BuiltOutputs: make(map[string]Realisation),
	}

	// Logic to be implemented in proxy.go, testing it here first
	if result.Status == BuildResultAlreadyValid || len(result.BuiltOutputs) == 0 {
		// Compute hash from derivation
		drvHash := ComputeDerivationHash(drv)

		for name, output := range drv.Outputs {
			id := fmt.Sprintf("sha256:%s!%s", drvHash, name)

			// For AlreadyValid, we can assume valid if the path exists
			real := Realisation{
				ID:                    id,
				OutPath:               output.Path,
				Signatures:            []string{},
				DependentRealisations: make(map[string]string),
			}
			result.BuiltOutputs[id] = real
		}
	}

	if len(result.BuiltOutputs) != 1 {
		t.Errorf("Expected 1 built output, got %d", len(result.BuiltOutputs))
	}

	for id, real := range result.BuiltOutputs {
		log.Printf("ID: %s, Path: %s", id, real.OutPath)
		if real.OutPath != drv.Outputs["out"].Path {
			t.Errorf("Expected path %s, got %s", drv.Outputs["out"].Path, real.OutPath)
		}
		if real.Signatures == nil {
			t.Errorf("Signatures is nil")
		}
		if real.DependentRealisations == nil {
			t.Errorf("DependentRealisations is nil")
		}

		// Verify JSON output
		b, _ := json.Marshal(real)
		jsonStr := string(b)
		log.Printf("JSON: %s", jsonStr)
		if !strings.Contains(jsonStr, "\"signatures\":[]") {
			t.Errorf("JSON does not contain empty signatures array: %s", jsonStr)
		}
		if !strings.Contains(jsonStr, "\"dependentRealisations\":{}") {
			t.Errorf("JSON does not contain empty dependentRealisations object: %s", jsonStr)
		}
	}
}
