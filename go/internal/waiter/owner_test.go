package waiter

import "testing"

func TestParseOwner(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		want    Owner
		wantErr bool
	}{
		{name: "valid", token: "123:456", want: Owner{PID: 123, Created: 456}},
		{name: "empty", token: "", wantErr: true},
		{name: "missing created", token: "123", wantErr: true},
		{name: "invalid pid", token: "abc:456", wantErr: true},
		{name: "zero pid", token: "0:456", wantErr: true},
		{name: "invalid created", token: "123:nope", wantErr: true},
		{name: "zero created", token: "123:0", wantErr: true},
		{name: "extra fields", token: "123:456:789", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOwner(tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseOwner(%q) error = %v, wantErr %t", tt.token, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("ParseOwner(%q) = %#v, want %#v", tt.token, got, tt.want)
			}
		})
	}
}
