package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/schema"
)

// SHAPE-FINGERPRINT (fitness, level: contract)
//
// A tripwire that freezes the WHOLE wire contract in one place so neither the
// published JSON Schema nor the Go Envelope struct can drift without the other
// (and without a reviewed lock update + a SchemaVersion bump). The lock file
// testdata/contract.lock.json IS the frozen fingerprint — there is deliberately
// no duplicate Go const, to avoid double-maintenance.
//
// It locks three things:
//   - SchemaVersion (the wire version embedded in every envelope),
//   - sha256 of the exact schema bytes (catches any schema edit), and
//   - a reflection-derived canonical struct shape + its sha256 (catches any
//     field rename / retype / reorder / omitempty change in Envelope or any
//     nested wire type).
//
// Non-vacuity (guard→mutant): adding a field to Envelope, renaming a json tag,
// retyping a field, or editing schema/envelope.schema.json — without
// regenerating the lock (WI_UPDATE_CONTRACT_LOCK=1) — turns TestContractFrozen
// RED. TestFingerprintIsNonVacuous proves the shape extractor genuinely detects
// added fields, retypes, and omitempty changes.

const updateLockEnv = "WI_UPDATE_CONTRACT_LOCK"

type contractLock struct {
	SchemaVersion     string `json:"schema_version"`
	SchemaSHA256      string `json:"schema_sha256"`
	StructShape       string `json:"struct_shape"`
	StructShapeSHA256 string `json:"struct_shape_sha256"`
}

func lockPath() string { return filepath.Join("testdata", "contract.lock.json") }

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// shapeOf renders a deterministic canonical signature of a wire type: struct
// fields are expanded in declaration order as `<jsonTag>:<shape>` (the json tag
// carries the wire name and any ,omitempty), recursing through pointers, slices,
// and nested structs. Non-struct types render as their Go type string, so a
// retype (e.g. string -> WarningCode) is a detectable change.
func shapeOf(t reflect.Type, seen map[reflect.Type]bool) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + shapeOf(t.Elem(), seen)
	case reflect.Slice:
		return "[]" + shapeOf(t.Elem(), seen)
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), shapeOf(t.Elem(), seen))
	case reflect.Struct:
		if seen[t] {
			return t.String() // break any cycle
		}
		seen[t] = true
		defer delete(seen, t) // re-expand same type in a sibling position
		var b strings.Builder
		b.WriteString("{")
		first := true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(f.Tag.Get("json"))
			b.WriteString(":")
			b.WriteString(shapeOf(f.Type, seen))
		}
		b.WriteString("}")
		return b.String()
	default:
		return t.String()
	}
}

func computeLock() contractLock {
	shape := shapeOf(reflect.TypeOf(Envelope{}), map[reflect.Type]bool{})
	return contractLock{
		SchemaVersion:     SchemaVersion,
		SchemaSHA256:      sha256hex(schema.EnvelopeSchema),
		StructShape:       shape,
		StructShapeSHA256: sha256hex([]byte(shape)),
	}
}

func TestContractFrozen(t *testing.T) {
	got := computeLock()

	if os.Getenv(updateLockEnv) == "1" {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath(), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s", lockPath())
		return
	}

	raw, err := os.ReadFile(lockPath())
	if err != nil {
		t.Fatalf("read contract lock: %v (regenerate with %s=1)", err, updateLockEnv)
	}
	var want contractLock
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse contract lock: %v", err)
	}

	if got.SchemaVersion != want.SchemaVersion {
		t.Errorf("SchemaVersion drift: got %q, locked %q", got.SchemaVersion, want.SchemaVersion)
	}
	if got.SchemaSHA256 != want.SchemaSHA256 {
		t.Errorf("schema bytes drifted (got %s, locked %s).\nIf intentional: bump SchemaVersion and regenerate with %s=1.",
			got.SchemaSHA256, want.SchemaSHA256, updateLockEnv)
	}
	if got.StructShapeSHA256 != want.StructShapeSHA256 {
		t.Errorf("Envelope struct shape drifted.\n got shape = %s\nlocked sha = %s, got sha = %s\nIf intentional: keep schema in lockstep, bump SchemaVersion, regenerate with %s=1.",
			got.StructShape, want.StructShapeSHA256, got.StructShapeSHA256, updateLockEnv)
	}
}

func TestFingerprintIsNonVacuous(t *testing.T) {
	type base struct {
		X string `json:"x"`
	}
	type added struct {
		X string `json:"x"`
		Y int    `json:"y"`
	}
	type retyped struct {
		X int `json:"x"`
	}
	type omitted struct {
		X string `json:"x,omitempty"`
	}
	fresh := func() map[reflect.Type]bool { return map[reflect.Type]bool{} }
	sBase := shapeOf(reflect.TypeOf(base{}), fresh())
	if s := shapeOf(reflect.TypeOf(added{}), fresh()); s == sBase {
		t.Error("shape extractor is blind to an added field")
	}
	if s := shapeOf(reflect.TypeOf(retyped{}), fresh()); s == sBase {
		t.Error("shape extractor is blind to a field retype")
	}
	if s := shapeOf(reflect.TypeOf(omitted{}), fresh()); s == sBase {
		t.Error("shape extractor is blind to an omitempty change")
	}
	// And the hash must move with the shape.
	if sha256hex([]byte(sBase)) == sha256hex([]byte(shapeOf(reflect.TypeOf(added{}), fresh()))) {
		t.Error("fingerprint hash is blind to a shape change")
	}
}
