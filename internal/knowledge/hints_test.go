package knowledge

import "testing"

func TestHintsFromPath(t *testing.T) {
	cases := []struct {
		path     string
		docType  string
		language string
		product  string
	}{
		{"/Users/gen/docs/policies/life/en/policy-xxx.pdf", "policy", "en", "life"},
		{"/data/promotions/motor/zh/flyer.png", "promotion", "zh", "motor"},
		{"C:\\Stuff\\Campaigns\\Business\\MS\\note.docx", "promotion", "ms", "business"},
		{"/tmp/random/file.txt", "", "", ""},
		{"/vault/Templates/Medical/EN/tmpl.md", "template", "en", "medical"},
		{"/x/TRAVEL/Chinese/doc.pdf", "", "zh", "travel"},
	}
	for _, tc := range cases {
		h := HintsFromPath(tc.path)
		if h.DocType != tc.docType || h.Language != tc.language || h.Product != tc.product {
			t.Errorf("HintsFromPath(%q) = {doc_type=%q, lang=%q, product=%q}; want {doc_type=%q, lang=%q, product=%q}",
				tc.path, h.DocType, h.Language, h.Product,
				tc.docType, tc.language, tc.product)
		}
	}
}
