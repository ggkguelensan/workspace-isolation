// Package invariants holds architecture-level fitness guards that constrain the
// whole module rather than any single package — the DESIGN.md §2 invariants.
//
// Today it owns INV-NO-LLM (no LLM/agent SDK anywhere in the dependency graph).
// Later it will host INV-NO-NETWORK (Go seam + git child-process belt) and the
// single-ErrorKind-owner architecture check. The package carries no runtime
// code; it exists solely to own these cross-cutting tests in one place.
package invariants
