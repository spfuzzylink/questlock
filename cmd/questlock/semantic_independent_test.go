package main

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestIndependentSuitesKeepFrozenLabelsAndParserContract(t *testing.T) {
	for _, fixture := range []struct {
		name, sha          string
		positive, negative int
	}{
		{"independent-evaluation.json", "36d9e0e1f6fef4a1985aa7632bdd99968384274792ca53a287b4540c584ab40a", 60, 60},
		{"unsupported-answer-evaluation.json", "1699cb7c7a6a4fea915abacb5e443808e608d52b5d843a7660aa37339e594def", 0, 12},
		{"independent-round2-evaluation.json", "519b5b7981f5ff79f3e2b5f380c5fb5ef8b6198284a5aaf22f31b87f31cd6a87", 60, 60},
		{"unsupported-round2-evaluation.json", "cad36fa26a2a49cda0817a9805572eecccbb824a18743f7e0cb6b9c305b79094", 0, 12},
		{"independent-round3-evaluation.json", "b496650e7986b4cf52bb1c3dbb1626c1e032862c80910644c769f490c2025128", 60, 60},
		{"unsupported-round3-evaluation.json", "7fbf8e5f18c5ec106096b5b9b67bcc13fe20c64bc9b6027e9b19a93ab1a3986d", 0, 12},
		{"independent-round4-evaluation.json", "4137899993b1ea6eb9650babe865b83cc564147dae4b32a166c48f7fae52cb3a", 60, 60},
		{"unsupported-round4-evaluation.json", "37b519c2e7927c8afa2fec38a60da44fbe4a7d1509b4591a30704a573260b195", 0, 12},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			suite, raw, err := loadSemanticEvalSuite(filepath.Join("..", "..", "examples", "semantic", fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.Sum256(raw)
			positive, negative := 0, 0
			for _, c := range suite.Cases {
				for _, v := range c.Variants {
					if *v.ShouldReuse {
						positive++
					} else {
						negative++
					}
				}
			}
			if hex.EncodeToString(h[:]) != fixture.sha || len(suite.Cases) != 6 || positive != fixture.positive || negative != fixture.negative {
				t.Fatal("frozen independent corpus changed")
			}
		})
	}
}
