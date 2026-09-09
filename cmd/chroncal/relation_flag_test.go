package main

import "testing"

// The database stores any RELTYPE token that a server sends. The flag parser
// must accept an x-name, or a --related-to edit drops a stored x-name
// relation and the next push sends the shortened body to the server.
func TestParseRelationFlagsAcceptsXNameToken(t *testing.T) {
	rels, err := parseRelationFlags([]string{"X-APPLE-SOMETHING:apple-uid"})
	if err != nil {
		t.Fatalf("parseRelationFlags: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d relations, want 1", len(rels))
	}
	if rels[0].RelType != "X-APPLE-SOMETHING" || rels[0].RelUID != "apple-uid" {
		t.Errorf("relation = %+v, want RelType X-APPLE-SOMETHING and RelUID apple-uid", rels[0])
	}
}

// A URN UID keeps its scheme. The parser must not read "urn" as a RELTYPE.
func TestParseRelationFlagsKeepsURNUID(t *testing.T) {
	rels, err := parseRelationFlags([]string{"urn:uuid:1234"})
	if err != nil {
		t.Fatalf("parseRelationFlags: %v", err)
	}
	if rels[0].RelType != "PARENT" || rels[0].RelUID != "urn:uuid:1234" {
		t.Errorf("relation = %+v, want RelType PARENT and the whole URN as the UID", rels[0])
	}
}

// The three named values still work, and the parser folds the case.
func TestParseRelationFlagsAcceptsNamedTypes(t *testing.T) {
	rels, err := parseRelationFlags([]string{"child:a", "SIBLING:b", "c"})
	if err != nil {
		t.Fatalf("parseRelationFlags: %v", err)
	}
	want := []struct{ relType, uid string }{{"CHILD", "a"}, {"SIBLING", "b"}, {"PARENT", "c"}}
	for i, w := range want {
		if rels[i].RelType != w.relType || rels[i].RelUID != w.uid {
			t.Errorf("relation %d = %+v, want %s:%s", i, rels[i], w.relType, w.uid)
		}
	}
}
