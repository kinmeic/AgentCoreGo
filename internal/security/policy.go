package security

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

// Effect determines what happens when a policy matches.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
	EffectAudit Effect = "audit" // Log but allow
)

// Resource identifies the type of action being evaluated.
type Resource string

const (
	ResourceFile    Resource = "file"
	ResourceTerminal Resource = "terminal"
	ResourceWeb     Resource = "web"
	ResourceMemory  Resource = "memory"
)

// Action represents a request to perform an operation.
type Action struct {
	Resource    Resource
	Operation   string // e.g., "read", "write", "execute", "fetch"
	Target      string // e.g., file path, command, URL
	ConvID      string
	Metadata    map[string]string
}

// Decision is the result of a policy evaluation.
type Decision struct {
	Allowed bool
	Reason  string
	Policy  string // Name of the matching policy
	Audit   bool   // Whether this was an audit-only match
}

// Condition defines a match criterion within a policy.
type Condition struct {
	Field    string   `yaml:"field"`    // "target", "operation"
	Operator string   `yaml:"operator"` // "contains", "prefix", "regex", "in"
	Values   []string `yaml:"values"`
}

// Policy defines a security rule.
type Policy struct {
	Name       string     `yaml:"name"`
	Effect     Effect     `yaml:"effect"`
	Resource   Resource   `yaml:"resource"`
	Conditions []Condition `yaml:"conditions"`
}

// PolicyEngine evaluates security policies against actions.
// Inspired by hermes-agent's Tirith security framework.
type PolicyEngine struct {
	mu       sync.RWMutex
	policies []Policy
}

// NewPolicyEngine creates a policy engine with the default policy set.
func NewPolicyEngine(customPolicies []Policy) *PolicyEngine {
	policies := DefaultPolicies()
	if len(customPolicies) > 0 {
		policies = append(policies, customPolicies...)
	}
	return &PolicyEngine{
		policies: policies,
	}
}

// Evaluate checks an action against all policies and returns a decision.
// Policies are evaluated in order; the first deny wins, then audit, then allow.
func (e *PolicyEngine) Evaluate(action Action) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var auditMatches, allowMatches []string

	for _, p := range e.policies {
		if p.Resource != action.Resource {
			continue
		}

		if !matchConditions(p.Conditions, action) {
			continue
		}

		switch p.Effect {
		case EffectDeny:
			return Decision{
				Allowed: false,
				Reason:  fmt.Sprintf("Denied by policy %q", p.Name),
				Policy:  p.Name,
			}
		case EffectAudit:
			auditMatches = append(auditMatches, p.Name)
		case EffectAllow:
			allowMatches = append(allowMatches, p.Name)
		}
	}

	// If any audit policies matched, log but allow
	if len(auditMatches) > 0 {
		allMatches := append(auditMatches, allowMatches...)
		log.Printf("[security] AUDIT: action %s/%s target=%q matched policies: %s",
			action.Resource, action.Operation, action.Target,
			strings.Join(allMatches, ", "))
		return Decision{
			Allowed: true,
			Reason:  "Allowed (audit logged)",
			Policy:  strings.Join(allMatches, ","),
			Audit:   true,
		}
	}

	// If explicit allow policies matched, log them
	if len(allowMatches) > 0 {
		log.Printf("[security] ALLOWED: action %s/%s target=%q matched policies: %s",
			action.Resource, action.Operation, action.Target,
			strings.Join(allowMatches, ", "))
		return Decision{
			Allowed: true,
			Reason:  "Allowed by policy",
			Policy:  strings.Join(allowMatches, ","),
		}
	}

	// Default allow with audit log for unmatched actions
	log.Printf("[security] UNMATCHED: action %s/%s target=%q — default allow",
		action.Resource, action.Operation, action.Target)
	return Decision{
		Allowed: true,
		Reason:  "No matching policy (default allow)",
		Audit:   true,
	}
}

// AddPolicy appends a policy at runtime.
func (e *PolicyEngine) AddPolicy(p Policy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies = append(e.policies, p)
}

// matchConditions returns true if ALL conditions in the policy match the action.
// Returns false if conditions is empty to prevent accidental match-all policies.
func matchConditions(conditions []Condition, action Action) bool {
	if len(conditions) == 0 {
		return false
	}
	for _, c := range conditions {
		if !matchCondition(c, action) {
			return false
		}
	}
	return true
}

func matchCondition(c Condition, action Action) bool {
	value := extractField(c.Field, action)

	switch c.Operator {
	case "contains":
		for _, v := range c.Values {
			if strings.Contains(strings.ToLower(value), strings.ToLower(v)) {
				return true
			}
		}
		return false

	case "prefix":
		lowerValue := strings.ToLower(value)
		for _, v := range c.Values {
			if strings.HasPrefix(lowerValue, strings.ToLower(v)) {
				return true
			}
		}
		return false

	case "suffix":
		lowerValue := strings.ToLower(value)
		for _, v := range c.Values {
			if strings.HasSuffix(lowerValue, strings.ToLower(v)) {
				return true
			}
		}
		return false

	case "equals":
		for _, v := range c.Values {
			if value == v {
				return true
			}
		}
		return false

	case "in":
		for _, v := range c.Values {
			if strings.EqualFold(value, v) {
				return true
			}
		}
		return false

	default:
		log.Printf("[security] WARNING: unknown policy operator %q", c.Operator)
		return false
	}
}

func extractField(field string, action Action) string {
	switch field {
	case "target":
		return action.Target
	case "operation":
		return action.Operation
	case "resource":
		return string(action.Resource)
	default:
		if v, ok := action.Metadata[field]; ok {
			return v
		}
		return ""
	}
}
