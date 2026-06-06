package security

// DefaultPolicies returns the built-in security policy set.
func DefaultPolicies() []Policy {
	return []Policy{
		// --- Terminal command safety ---
		{
			Name:     "deny-destructive-commands",
			Effect:   EffectDeny,
			Resource: ResourceTerminal,
			Conditions: []Condition{
				{
					Field:    "target",
					Operator: "contains",
					Values: []string{
						"rm -rf /",
						"rm -rf /*",
						"mkfs",
						"dd if=/dev/zero of=/dev/",
						":(){:|:&};:",  // fork bomb
						"chmod -R 777 /",
					},
				},
			},
		},
		{
			Name:     "audit-sudo-commands",
			Effect:   EffectAudit,
			Resource: ResourceTerminal,
			Conditions: []Condition{
				{
					Field:    "target",
					Operator: "prefix",
					Values:   []string{"sudo "},
				},
			},
		},
		{
			Name:     "deny-curl-pipe-sh",
			Effect:   EffectAudit,
			Resource: ResourceTerminal,
			Conditions: []Condition{
				{
					Field:    "target",
					Operator: "contains",
					Values: []string{
						"| sh",
						"| bash",
						"| zsh",
						"|sh",
						"|bash",
					},
				},
			},
		},

		// --- File operation safety ---
		{
			Name:     "audit-sensitive-file-write",
			Effect:   EffectAudit,
			Resource: ResourceFile,
			Conditions: []Condition{
				{
					Field:    "operation",
					Operator: "equals",
					Values:   []string{"write"},
				},
				{
					Field:    "target",
					Operator: "contains",
					Values: []string{
						".env",
						".ssh",
						"id_rsa",
						"id_ed25519",
						"credentials",
						"secret",
						".aws",
						".kube/config",
					},
				},
			},
		},

		// --- Web access safety ---
		{
			Name:     "deny-metadata-service",
			Effect:   EffectDeny,
			Resource: ResourceWeb,
			Conditions: []Condition{
				{
					Field:    "target",
					Operator: "contains",
					Values: []string{
						"169.254.169.254",
						"metadata.google.internal",
						"metadata.azure.com",
					},
				},
			},
		},
	}
}
