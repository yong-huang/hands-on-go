package main

import (
	"fmt"
	"strings"
)

func main() {
	// Arrays - fixed length
	var arr1 [3]int = [3]int{1, 2, 3}
	arr2 := [5]string{"a", "b", "c", "d", "e"}
	arr3 := [...]float64{1.1, 2.2, 3.3} // size inferred

	fmt.Println("=== Arrays (fixed length) ===")
	fmt.Printf("arr1: %v, length: %d, capacity: %d\n", arr1, len(arr1), cap(arr1))
	fmt.Printf("arr2: %v, length: %d, capacity: %d\n", arr2, len(arr2), cap(arr2))
	fmt.Printf("arr3: %v, length: %d, capacity: %d\n", arr3, len(arr3), cap(arr3))

	// Slices - dynamic length
	var slice1 []int = []int{1, 2, 3, 4, 5}
	slice2 := []string{"hello", "world"}
	slice3 := make([]int, 3, 5) // length 3, capacity 5

	fmt.Println("\n=== Slices (dynamic length) ===")
	fmt.Printf("slice1: %v, length: %d, capacity: %d\n", slice1, len(slice1), cap(slice1))
	fmt.Printf("slice2: %v, length: %d, capacity: %d\n", slice2, len(slice2), cap(slice2))
	fmt.Printf("slice3: %v, length: %d, capacity: %d\n", slice3, len(slice3), cap(slice3))

	// Slices grow dynamically
	slice1 = append(slice1, 6, 7, 8)
	fmt.Printf("After append - slice1: %v, length: %d, capacity: %d\n", slice1, len(slice1), cap(slice1))

	// Slicing an array
	subSlice := arr2[1:4]
	fmt.Printf("Sub-slice arr2[1:4]: %v, length: %d, capacity: %d\n", subSlice, len(subSlice), cap(subSlice))

	// Map declaration and initialization
	var map1 map[string]int      // nil map
	map2 := make(map[string]int) // empty map using make
	map3 := map[string]int{      // map literal
		"apple":  5,
		"banana": 3,
		"orange": 7,
	}

	fmt.Println("\n=== Maps ===")
	fmt.Printf("map1 is nil: %v\n", map1 == nil)
	fmt.Printf("map2: %v, length: %d\n", map2, len(map2))
	fmt.Printf("map3: %v, length: %d\n", map3, len(map3))

	// Adding and updating map entries
	map2["grape"] = 10
	map2["melon"] = 6
	map2["grape"] = 12 // update existing
	fmt.Printf("map2 after adding: %v\n", map2)

	// Accessing map entries
	value, exists := map2["grape"]
	fmt.Printf("map2['grape'] = %d, exists: %v\n", value, exists)

	value2, exists2 := map2["kiwi"]
	fmt.Printf("map2['kiwi'] = %d, exists: %v\n", value2, exists2)

	// Deleting from map
	delete(map2, "melon")
	fmt.Printf("map2 after delete: %v\n", map2)

	// Word counter - count frequency of words in a text
	text := "go is a statically typed compiled programming language go is simple and efficient go has garbage collection"
	wordCount := wordCounter(text)

	fmt.Println("\n=== Word Counter ===")
	fmt.Printf("Text: %s\n", text)
	fmt.Printf("Word count: %v\n", wordCount)
}

// wordCounter splits text by spaces and returns a map of word frequencies
func wordCounter(text string) map[string]int {
	words := strings.Fields(text)
	counter := make(map[string]int)

	for _, word := range words {
		// Normalize word to lowercase for case-insensitive counting
		word = strings.ToLower(word)
		counter[word]++
	}
	return counter
}
