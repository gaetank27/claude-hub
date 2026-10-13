package rag

import (
	"math"
	"sort"

	"system/internal/store"
)

// Scored is a retrieved chunk with its cosine similarity to the query.
type Scored struct {
	store.RagChunkHit
	Score float32
}

// TopK ranks hits by cosine similarity to query and returns the best k. Vectors
// of a different dimension than the query (e.g. embedded under a previous model)
// are skipped rather than mis-scored.
func TopK(query []float32, hits []store.RagChunkHit, k int) []Scored {
	qn := norm(query)
	if qn == 0 {
		return nil
	}
	scored := make([]Scored, 0, len(hits))
	for _, h := range hits {
		if len(h.Embedding) != len(query) {
			continue
		}
		s := cosine(query, h.Embedding, qn)
		scored = append(scored, Scored{RagChunkHit: h, Score: s})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if k > 0 && len(scored) > k {
		scored = scored[:k]
	}
	return scored
}

// cosine computes the cosine similarity of a and b, given a's precomputed norm.
func cosine(a, b []float32, an float64) float32 {
	var dot, bn float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bn += float64(b[i]) * float64(b[i])
	}
	if bn == 0 {
		return 0
	}
	return float32(dot / (an * math.Sqrt(bn)))
}

func norm(v []float32) float64 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	return math.Sqrt(n)
}
