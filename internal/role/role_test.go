package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleValidate(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		wantErr string // empty = no error
	}{
		{
			name:    "valid minimal",
			role:    Role{Name: "makro", Description: "Makro dev"},
			wantErr: "",
		},
		{
			name:    "missing name",
			role:    Role{Description: "x"},
			wantErr: "role name is required",
		},
		{
			name:    "missing description",
			role:    Role{Name: "makro"},
			wantErr: "role description is required",
		},
		{
			name:    "name with space rejected",
			role:    Role{Name: "my role", Description: "x"},
			wantErr: "role name must be identifier-safe",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.role.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestRoleClearAfterDefault(t *testing.T) {
	r := Role{Name: "r", Description: "d"}
	r.applyDefaults()
	assert.Equal(t, ClearAfterManual, r.ClearAfter)
}

func TestRoleStateFileTemplate(t *testing.T) {
	r := Role{Name: "reviewer", Description: "d", StateFile: "reviewer/{project}.md"}
	got, err := r.ResolveStateFile("makro")
	require.NoError(t, err)
	assert.Equal(t, "reviewer/makro.md", got)
}

func TestRoleStateFileEmpty(t *testing.T) {
	r := Role{Name: "r", Description: "d"}
	got, err := r.ResolveStateFile("makro")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}
