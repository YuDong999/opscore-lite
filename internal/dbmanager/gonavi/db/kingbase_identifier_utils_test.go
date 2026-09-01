package db

import "testing"

func TestNormalizeKingbaseIdentCommon(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "ldf_server", want: "ldf_server"},
		{name: "quoted", in: `"ldf_server"`, want: "ldf_server"},
		{name: "escaped quoted", in: `\"ldf_server\"`, want: "ldf_server"},
		{name: "double escaped quoted", in: `\\\"ldf_server\\\"`, want: "ldf_server"},
		{name: "double quoted", in: `""ldf_server""`, want: "ldf_server"},
		{name: "backtick quoted", in: "`ldf_server`", want: "ldf_server"},
		{name: "bracket quoted", in: "[ldf_server]", want: "ldf_server"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeKingbaseIdentCommon(tt.in); got != tt.want {
				t.Fatalf("normalizeKingbaseIdentCommon(%q)=%q,want=%q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitKingbaseQualifiedNameCommon(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantSchema string
		wantTable  string
	}{
		{name: "plain", in: "ldf_server.andon_events", wantSchema: "ldf_server", wantTable: "andon_events"},
		{name: "quoted", in: `"ldf_server"."andon_events"`, wantSchema: "ldf_server", wantTable: "andon_events"},
		{name: "escaped quoted", in: `\"ldf_server\".\"andon_events\"`, wantSchema: "ldf_server", wantTable: "andon_events"},
		{name: "double escaped quoted", in: `\\\"ldf_server\\\".\\\"andon_events\\\"`, wantSchema: "ldf_server", wantTable: "andon_events"},
		{name: "space around dot", in: ` "ldf_server" . "andon_events" `, wantSchema: "ldf_server", wantTable: "andon_events"},
		{name: "table only", in: "andon_events", wantSchema: "", wantTable: "andon_events"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSchema, gotTable := splitKingbaseQualifiedNameCommon(tt.in)
			if gotSchema != tt.wantSchema || gotTable != tt.wantTable {
				t.Fatalf("splitKingbaseQualifiedNameCommon(%q)=(%q,%q),want=(%q,%q)", tt.in, gotSchema, gotTable, tt.wantSchema, tt.wantTable)
			}
		})
	}
}

func TestSplitSQLQualifiedName(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantSchema string
		wantTable  string
	}{
		{name: "plain", in: "sales.orders", wantSchema: "sales", wantTable: "orders"},
		{name: "quoted dots", in: `"sales.schema"."order.items"`, wantSchema: "sales.schema", wantTable: "order.items"},
		{name: "escaped quoted dots", in: `\"sales.schema\".\"order.items\"`, wantSchema: "sales.schema", wantTable: "order.items"},
		{name: "quoted table only with dot", in: `"order.items"`, wantSchema: "", wantTable: "order.items"},
		{name: "escaped quoted", in: `\"sales\".\"orders\"`, wantSchema: "sales", wantTable: "orders"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSchema, gotTable := SplitSQLQualifiedName(tt.in)
			if gotSchema != tt.wantSchema || gotTable != tt.wantTable {
				t.Fatalf("SplitSQLQualifiedName(%q)=(%q,%q),want=(%q,%q)", tt.in, gotSchema, gotTable, tt.wantSchema, tt.wantTable)
			}
		})
	}
}

func TestBuildKingbaseSearchPathCommon(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    string
		wantLen int
	}{
		{
			name:    "normal schemas",
			in:      []string{"ldf_server", "public"},
			want:    `"ldf_server", "public"`,
			wantLen: 2,
		},
		{
			name:    "quoted and escaped schemas should not be double quoted",
			in:      []string{`"ldf_server"`, `""bcs_barcode""`, `\"public\"`},
			want:    `"ldf_server", "bcs_barcode", "public"`,
			wantLen: 3,
		},
		{
			name:    "dedupe ignoring case and keep public fallback",
			in:      []string{"LDF_SERVER", "ldf_server", "PUBLIC"},
			want:    `"LDF_SERVER", "public"`,
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, parts := buildKingbaseSearchPathCommon(tt.in)
			if got != tt.want {
				t.Fatalf("buildKingbaseSearchPathCommon(%v)=%q,want=%q", tt.in, got, tt.want)
			}
			if len(parts) != tt.wantLen {
				t.Fatalf("buildKingbaseSearchPathCommon(%v) parts=%v, len=%d, wantLen=%d", tt.in, parts, len(parts), tt.wantLen)
			}
		})
	}
}
