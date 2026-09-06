package logmonitor

import "testing"

func TestNormalizeSvcName(t *testing.T) {
	cases := map[string]string{
		"nginx-6d664c6d47-s7ljj":                 "nginx",
		"halo-66859784d5-fvh9r":                  "halo",
		"smart-alert-aggregator-b6ccff87d-scsfm": "smart-alert-aggregator",
		"nginx-exporter-59fcccc856-lrqpk":        "nginx-exporter",
		"halo-0":                                  "halo",
		"halo":                                    "halo",
		"order-api":                               "order-api",
		"fluentd":                                 "fluentd",
		"mysql":                                   "mysql",
		"":                                        "",
	}
	for in, want := range cases {
		if got := NormalizeSvcName(in); got != want {
			t.Errorf("NormalizeSvcName(%q) = %q, want %q", in, got, want)
		}
	}
}