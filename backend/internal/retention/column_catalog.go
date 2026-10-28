package retention

import (
	"slices"
	"strings"
)

type Privacy string

const (
	Structural    Privacy = "STRUCTURAL"
	PrivateBody   Privacy = "PRIVATE_BODY"
	PrivateText   Privacy = "PRIVATE_TEXT"
	PrivateKey    Privacy = "PRIVATE_KEY"
	Credential    Privacy = "CREDENTIAL"
	SharedPrivate Privacy = "WORKSPACE_PRIVATE"
)

type Handling string

const (
	KeepValidated                Handling = "VALIDATED_SHELL"
	ReplaceEnvelope              Handling = "INLINE_ENVELOPE"
	ClearText                    Handling = "CLEAR_SCALAR"
	FinancialProjection          Handling = "TYPED_FINANCIAL"
	DerivedProjection            Handling = "TYPED_DERIVED"
	AnonymizationRequired        Handling = "ANONYMIZE_BLOCKED"
	CredentialRevocationRequired Handling = "CREDENTIAL_LIFECYCLE"
	WorkspaceLifecycleRequired   Handling = "EXTERNAL_SCOPE"
)

type ColumnShape string

const (
	ShapeID         ColumnShape = "ID32"
	ShapeHash       ColumnShape = "HASH64"
	ShapeCommit     ColumnShape = "HASH40"
	ShapeInteger    ColumnShape = "NONNEG_INTEGER"
	ShapeBoolean    ColumnShape = "BOOLEAN"
	ShapeTime       ColumnShape = "SOURCE_TIME"
	ShapeTag        ColumnShape = "CLOSED_TAG"
	ShapeVersion    ColumnShape = "VERSION_TOKEN"
	ShapeJSON       ColumnShape = "TYPED_JSON"
	ShapeText       ColumnShape = "PRIVATE_TEXT"
	ShapeBytes      ColumnShape = "PRIVATE_BYTES"
	ShapeCredential ColumnShape = "CREDENTIAL_REF"
)

type OwnerScope string

const (
	ScopeWorkspace       OwnerScope = "W"
	ScopeNovelSetup      OwnerScope = "novel-setup"
	ScopeNovel           OwnerScope = "novel"
	ScopeProjectSelf     OwnerScope = "self"
	ScopeDirect          OwnerScope = "direct"
	ScopeOperationResult OwnerScope = "operation-result"
	ScopeAuditResource   OwnerScope = "audit-resource"
	ScopeBranch          OwnerScope = "branch"
	ScopeCandidate       OwnerScope = "candidate"
	ScopeResource        OwnerScope = "resource"
	ScopeKnowledgeIndex  OwnerScope = "knowledge-index"
	ScopeWorkingIndex    OwnerScope = "working-index"
	ScopeGeneration      OwnerScope = "generation"
	ScopeImport          OwnerScope = "import"
	ScopeCall            OwnerScope = "call"
	ScopePlan            OwnerScope = "plan"
	ScopeDirectRun       OwnerScope = "direct+run"
	ScopeDirectBranch    OwnerScope = "direct+branch"
	ScopeDirectImport    OwnerScope = "direct+import"
	ScopeDirectRecipe    OwnerScope = "direct+recipe"
	ScopeDirectRunCall   OwnerScope = "direct+run+call"
	ScopeDirectCandidate OwnerScope = "direct+candidate"
)

// ColumnPolicy classifies source bytes. It is not an installed materializer or
// a permit to erase: CLEAR still needs ClearScalarAllowed, financial/derived
// data needs a typed converter, and blocked keys need a reviewed identity map.
type ColumnPolicy struct {
	Column   string
	Privacy  Privacy
	Handling Handling
	Class    Class
	Shape    ColumnShape
}
type OwnerContract struct {
	Table      string
	Scope      OwnerScope
	Columns    []ColumnPolicy
	PrimaryKey []KeyColumn
}

func (p ColumnPolicy) Valid() bool {
	if p.Column == "" || !slices.Contains([]Privacy{Structural, PrivateBody, PrivateText, PrivateKey, Credential, SharedPrivate}, p.Privacy) || !slices.Contains([]Handling{KeepValidated, ReplaceEnvelope, ClearText, FinancialProjection, DerivedProjection, AnonymizationRequired, CredentialRevocationRequired, WorkspaceLifecycleRequired}, p.Handling) || !slices.Contains([]Class{Content, Candidate, RawRun, Debug, Audit, ProtectedAuthority, Temporary, FailedMedia, Backup}, p.Class) || !slices.Contains([]ColumnShape{ShapeID, ShapeHash, ShapeCommit, ShapeInteger, ShapeBoolean, ShapeTime, ShapeTag, ShapeVersion, ShapeJSON, ShapeText, ShapeBytes, ShapeCredential}, p.Shape) {
		return false
	}
	switch p.Privacy {
	case Structural:
		return p.Handling == KeepValidated && p.Shape != ShapeText && p.Shape != ShapeBytes && p.Shape != ShapeCredential
	case PrivateKey:
		return p.Handling == AnonymizationRequired
	case Credential:
		return p.Handling == CredentialRevocationRequired || p.Handling == WorkspaceLifecycleRequired
	case SharedPrivate:
		return p.Handling == WorkspaceLifecycleRequired
	case PrivateText:
		return p.Handling == ClearText || p.Handling == AnonymizationRequired
	case PrivateBody:
		return p.Handling == ReplaceEnvelope || p.Handling == FinancialProjection || p.Handling == DerivedProjection
	}
	return false
}

type ownerMetadata struct {
	contract OwnerContract
	columns  map[string]ColumnPolicy
}

// Built once from compiled contracts, never from source rows or persisted data.
// The maps and their backing slices stay private and are read-only after init.
var ownerMetadataByTable = func() map[string]ownerMetadata {
	index := make(map[string]ownerMetadata, len(reviewedColumnContracts))
	for _, spec := range reviewedColumnContracts {
		spec.PrimaryKey = PrimaryKey(spec.Table)
		columns := make(map[string]ColumnPolicy, len(spec.Columns))
		for _, policy := range spec.Columns {
			columns[policy.Column] = policy
		}
		index[spec.Table] = ownerMetadata{contract: spec, columns: columns}
	}
	return index
}()

// OwnerSpec returns a defensive copy of the reviewed schema31 source contract.
// Scope resolution/typed shape validation and current access checks are still
// required by the SQL scanner; table coverage alone never marks a scan complete.
func OwnerSpec(table string) (OwnerContract, bool) {
	metadata, ok := ownerMetadataByTable[table]
	if !ok {
		return OwnerContract{}, false
	}
	result := metadata.contract
	result.Columns = slices.Clone(result.Columns)
	result.PrimaryKey = slices.Clone(result.PrimaryKey)
	return result, true
}
func Column(table, column string) (ColumnPolicy, bool) {
	policy, ok := ownerMetadataByTable[table].columns[column]
	return policy, ok
}
func ValidateColumnCoverage(table string, columns []string) error {
	metadata, ok := ownerMetadataByTable[table]
	if !ok || len(columns) != len(metadata.contract.Columns) {
		return ErrInvalidInventory
	}
	seen := map[string]bool{}
	for _, column := range columns {
		if seen[column] {
			return ErrInvalidInventory
		}
		if _, ok := metadata.columns[column]; !ok {
			return ErrInvalidInventory
		}
		seen[column] = true
	}
	return nil
}

func columnGroup(privacy Privacy, handling Handling, class Class, shape ColumnShape, names string) []ColumnPolicy {
	var columns []ColumnPolicy
	for _, name := range strings.Fields(names) {
		columns = append(columns, ColumnPolicy{Column: name, Privacy: privacy, Handling: handling, Class: class, Shape: shape})
	}
	return columns
}
func ownerColumns(table string, scope OwnerScope, groups ...[]ColumnPolicy) OwnerContract {
	result := OwnerContract{Table: table, Scope: scope}
	for _, group := range groups {
		result.Columns = append(result.Columns, group...)
	}
	return result
}
