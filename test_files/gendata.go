package main

// Generate data files for testing.

import (
	"encoding/csv"
	"fmt"
	"math/rand"
	"os"
)

func file1() {

	r := rand.New(rand.NewSource(99))

	fid, err := os.Create("test1.csv")
	if err != nil {
		panic("Unable to open file.")
	}

	w := csv.NewWriter(fid)

	ncol := 10
	rowdata := make([]string, ncol)

	words := []string{"apple", "dog", "pear", "crocodile", "banana"}
	dates := []string{"19850621", "20001101", "19681010", "19731116"}

	nrow := 10000
	for i := 0; i < nrow; i++ {

		for j := 0; j < ncol; j++ {
			switch j % 4 {
			case 0:
				rowdata[j] = fmt.Sprintf("%.3f", r.Float64())
			case 1:
				rowdata[j] = words[r.Int63n(4)]
			case 2:
				rowdata[j] = fmt.Sprintf("%d", r.Int63n(100))
			case 3:
				rowdata[j] = dates[r.Int63n(4)]
			}
		}

		w.Write(rowdata)
	}

	w.Flush()
}

func main() {

	file1()

}
