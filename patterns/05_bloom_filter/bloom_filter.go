package main

import (
	"fmt"
	"hash/fnv"
	"math"
)

// BloomFilter is a probabilistic data structure for set membership testing.
// It may return false positives, but never false negatives.
type BloomFilter struct {
	bitArray  []uint64 // bit array for the filter
	size      uint     // number of bits in the bit array
	numHashes uint     // number of hash functions (k)
	count     uint     // number of items added
}

// NewBloomFilter creates a Bloom filter optimized for n items with target false positive rate ~1%.
// Formula: m = -(n * ln(p)) / (ln(2)^2), k = (m/n) * ln(2)
func NewBloomFilter(n uint, k uint) *BloomFilter {
	m := n * 10 // bit array size = 10 bits per element (good default)
	if k == 0 {
		k = uint(float64(m) / float64(n) * math.Ln2)
		if k < 1 {
			k = 1
		}
	}
	bitArray := make([]uint64, (m+63)/64) // ceil(m/64) uint64 words
	return &BloomFilter{
		bitArray:  bitArray,
		size:      m,
		numHashes: k,
		count:     0,
	}
}

// hash generates k hash values for a given item using double hashing technique:
// h_i(item) = h1(item) + i * h2(item)
func (bf *BloomFilter) hash(item string) []uint {
	h1 := fnvHash(item, 0)
	h2 := fnvHash(item, 0xBEEF) // different seed for second hash
	indices := make([]uint, bf.numHashes)
	for i := uint(0); i < bf.numHashes; i++ {
		indices[i] = (h1 + i*h2) % bf.size
	}
	return indices
}

// fnvHash computes FNV-1a hash with an optional seed offset.
func fnvHash(item string, seed uint) uint {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s%d", item, seed) // incorporate seed into input
	return uint(h.Sum64())
}

// setBit sets the bit at position pos in the bit array.
func (bf *BloomFilter) setBit(pos uint) {
	word := pos / 64
	bit := pos % 64
	bf.bitArray[word] |= 1 << bit
}

// getBit checks if the bit at position pos is set.
func (bf *BloomFilter) getBit(pos uint) bool {
	word := pos / 64
	bit := pos % 64
	return (bf.bitArray[word] & (1 << bit)) != 0
}

// Add inserts an item into the Bloom filter.
func (bf *BloomFilter) Add(item string) {
	for _, pos := range bf.hash(item) {
		bf.setBit(pos)
	}
	bf.count++
}

// MayContain checks if an item might be in the filter.
// Returns false if definitely not in the filter, true if possibly in the filter.
func (bf *BloomFilter) MayContain(item string) bool {
	for _, pos := range bf.hash(item) {
		if !bf.getBit(pos) {
			return false // definitely not in the set
		}
	}
	return true // possibly in the set (could be false positive)
}

// FalsePositiveRate estimates the current false positive rate based on
// number of items added and filter parameters.
// Formula: (1 - e^(-kn/m))^k
func (bf *BloomFilter) FalsePositiveRate() float64 {
	if bf.count == 0 {
		return 0
	}
	exponent := -float64(bf.numHashes) * float64(bf.count) / float64(bf.size)
	fpRate := math.Pow(1-math.Exp(exponent), float64(bf.numHashes))
	return fpRate
}

// Count returns the number of items added to the filter.
func (bf *BloomFilter) Count() uint {
	return bf.count
}

// Size returns the number of bits in the filter.
func (bf *BloomFilter) Size() uint {
	return bf.size
}

// demo runs a demonstration of Bloom filter operations and false positive testing.
func demo() {
	fmt.Println("=== Bloom Filter Demo ===")
	fmt.Println()

	// Create a bloom filter for 1000 expected items
	bf := NewBloomFilter(1000, 7)
	fmt.Printf("Filter: size=%d bits, numHashes=%d\n", bf.Size(), bf.numHashes)

	// Add items
	words := []string{"apple", "banana", "cherry", "date", "elderberry"}
	fmt.Println("\n--- Adding items ---")
	for _, w := range words {
		bf.Add(w)
		fmt.Printf("  Add(\"%s\")\n", w)
	}

	// Check existing items
	fmt.Println("\n--- Query existing items (should all be true) ---")
	for _, w := range words {
		result := bf.MayContain(w)
		fmt.Printf("  MayContain(\"%s\") = %v\n", w, result)
	}

	// Check non-existing items
	fmt.Println("\n--- Query non-existing items (false positive test) ---")
	nonWords := []string{"grape", "kiwi", "mango", "orange", "peach",
		"fig", "lemon", "melon", "plum", "berry"}
	falsePositives := 0
	for _, w := range nonWords {
		result := bf.MayContain(w)
		fmt.Printf("  MayContain(\"%s\") = %v\n", w, result)
		if result {
			falsePositives++
		}
	}

	fmt.Printf("\nFalse positives: %d / %d = %.2f%%\n",
		falsePositives, len(nonWords),
		float64(falsePositives)/float64(len(nonWords))*100)
	fmt.Printf("Estimated FP rate (formula): %.4f%%\n", bf.FalsePositiveRate()*100)

	// Large-scale false positive test
	fmt.Println("\n=== Large-Scale False Positive Test ===")
	bf2 := NewBloomFilter(10000, 7)
	for i := 0; i < 10000; i++ {
		bf2.Add(fmt.Sprintf("element_%d", i))
	}
	fpCount := 0
	testSize := 10000
	for i := 0; i < testSize; i++ {
		if bf2.MayContain(fmt.Sprintf("nonexistent_%d", i)) {
			fpCount++
		}
	}
	fmt.Printf("Added: %d items, Filter bits: %d, Hash functions: %d\n",
		bf2.Count(), bf2.Size(), bf2.numHashes)
	fmt.Printf("Tested: %d non-existing items\n", testSize)
	fmt.Printf("False positives: %d / %d = %.2f%%\n",
		fpCount, testSize, float64(fpCount)/float64(testSize)*100)
	fmt.Printf("Estimated FP rate (formula): %.4f%%\n", bf2.FalsePositiveRate()*100)

	// Trade-off analysis: different numHashes
	fmt.Println("\n=== Hash Function Count Trade-off ===")
	for k := uint(1); k <= 10; k++ {
		bf3 := NewBloomFilter(10000, k)
		for i := 0; i < 10000; i++ {
			bf3.Add(fmt.Sprintf("item_%d", i))
		}
		fp := 0
		for i := 0; i < 10000; i++ {
			if bf3.MayContain(fmt.Sprintf("nope_%d", i)) {
				fp++
			}
		}
		fmt.Printf("  k=%2d  bits=%-5d  actual FP=%.2f%%  estimated FP=%.2f%%\n",
			k, bf3.Size(),
			float64(fp)/float64(10000)*100,
			bf3.FalsePositiveRate()*100)
	}
}

func main() {
	demo()
}
