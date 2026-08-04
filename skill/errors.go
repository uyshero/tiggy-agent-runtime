package skill

import "fmt"

type ErrorCode string

const (
	ErrorInvalidConfig   ErrorCode = "invalid_config"
	ErrorInvalidManifest ErrorCode = "invalid_manifest"
	ErrorCatalogLimit    ErrorCode = "catalog_limit"
	ErrorSkillNotFound   ErrorCode = "skill_not_found"
	ErrorResourceMissing ErrorCode = "resource_not_found"
	ErrorActivationLimit ErrorCode = "activation_limit"
	ErrorInvalidContent  ErrorCode = "invalid_content"
	ErrorSourceFailed    ErrorCode = "source_failed"
)

type Error struct {
	Code      ErrorCode
	Operation string
	Skill     string
	Path      string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return "skill operation failed"
	}
	if e.Skill != "" {
		return fmt.Sprintf("skill %s %q failed (%s)", e.Operation, e.Skill, e.Code)
	}
	return fmt.Sprintf("skill %s failed (%s)", e.Operation, e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
