package datareader

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

var supported_dta_versions = []int{114, 115, 117, 118}

var row_count_width = map[int]int{114: 4, 115: 4, 117: 4, 118: 8}

var nvar_width = map[int]int{114: 2, 115: 2, 117: 2, 118: 2}

var dataset_label_width = map[int]int{117: 1, 118: 2}

// A StataReader reads Stata dta data files.  Currently versions
// 114-118 of Stata dta files can be read.  Not all fields in the
// StataReader struct are applicable to all file formats.
//
// The Read method reads and returns the data.  Several fields of the
// StataReader struct may also be of interest.
//
// Technical information about the file format can be found here:
// http://www.stata.com/help.cgi?dta
type StataReader struct {

	// If true, the strl numerical codes are replaced with their
	// string values when available.
	InsertStrls bool

	// If true, the categorial numerical codes are replaced with
	// their string labels when available.
	InsertCategoryLabels bool

	// If true, dates are converted to Go date format.
	ConvertDates bool

	// A short text label for the data set.
	DatasetLabel string

	// The time stamp for the data set
	TimeStamp string

	// Number of variables
	Nvar int

	// Number of observations
	RowCount int

	// Variable types, see technical documentation for meaning
	VarTypes []uint16

	// A name for each variable
	column_names []string

	// An additional text entry describing each variable
	ColumnNamesLong []string

	// String labels for categorical variables
	ValueLabels     map[string]map[int32]string
	ValueLabelNames []string

	// Format codes for each variable
	Formats []string

	// Maps from strl keys to values
	Strls      map[uint64]string
	StrlsBytes map[uint64][]byte

	// The format version of the dta file
	FormatVersion int

	// The endian-ness of the file
	ByteOrder binary.ByteOrder

	// The number of rows of data that have been read.
	rows_read int

	// Map information
	seek_vartypes          int64
	seek_varnames          int64
	seek_sortlist          int64
	seek_formats           int64
	seek_value_label_names int64
	seek_variable_labels   int64
	seek_characteristics   int64
	seek_data              int64
	seek_strls             int64
	seek_value_labels      int64

	// Indicates the columns that contain dates
	is_date []bool

	// An io channel from which the data are read
	reader io.ReadSeeker
}

// NewStataReader returns a StataReader for reading from the given io channel.
func NewStataReader(r io.ReadSeeker) (*StataReader, error) {
	rdr := new(StataReader)
	rdr.reader = r

	// Defaults
	rdr.InsertStrls = true
	rdr.InsertCategoryLabels = true
	rdr.ConvertDates = true

	err := rdr.init()
	if err != nil {
		return nil, err
	}
	return rdr, nil
}

func (rdr *StataReader) ColumnNames() []string {
	return rdr.column_names
}

func (rdr *StataReader) init() error {

	// Determine if we have <117 or >=117 dta version.
	c := make([]byte, 1)
	rdr.reader.Read(c)
	rdr.reader.Seek(0, 0)

	var err error
	if string(c) == "<" {
		err = rdr.read_new_header()
	} else {
		err = rdr.read_old_header()
	}
	if err != nil {
		return err
	}

	rdr.read_vartypes()

	if rdr.FormatVersion < 117 {
		rdr.translate_vartypes()
	}

	rdr.read_varnames()

	// Skip over srtlist
	if rdr.FormatVersion < 117 {
		m := int64(2 * (rdr.Nvar + 1))
		rdr.reader.Seek(m, 1)
	}

	rdr.read_formats()
	rdr.read_value_label_names()
	rdr.read_variable_labels()

	if rdr.FormatVersion < 117 {
		rdr.read_expansion_fields()
	}

	if rdr.FormatVersion >= 117 {
		rdr.read_strls()

		// Must be called manually for older format < 117.
		rdr.read_value_labels()
	}

	return nil
}

func (rdr *StataReader) read_expansion_fields() {
	var b byte
	var i int32

	for {
		binary.Read(rdr.reader, rdr.ByteOrder, &b)
		binary.Read(rdr.reader, rdr.ByteOrder, &i)

		if (b == 0) && (i == 0) {
			break
		}
		rdr.reader.Seek(int64(i), 1)
	}
}

func (rdr *StataReader) read_int(width int) int {

	switch width {
	default:
		os.Stderr.WriteString(fmt.Sprintf("unsupported width %d in read_int", width))
		panic("")
	case 1:
		var x int8
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	case 2:
		var x int16
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	case 4:
		var x int32
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	case 8:
		var x int64
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	}
}

func (rdr *StataReader) read_uint(width int) int {

	switch width {
	default:
		panic("unsupported width in read_int")
	case 1:
		var x uint8
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	case 2:
		var x uint16
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	case 4:
		var x uint32
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	case 8:
		var x uint64
		binary.Read(rdr.reader, rdr.ByteOrder, &x)
		return int(x)
	}
}

// read_old_header reads the pre version 117 header
func (rdr *StataReader) read_old_header() error {

	buf := make([]byte, 81)

	// Get the format
	var format uint8
	binary.Read(rdr.reader, binary.LittleEndian, &format)

	rdr.FormatVersion = int(format)
	if !rdr.supported_version() {
		return errors.New("Invalid Stata dta format version")
	}

	// Get the byte order
	var bo uint8
	binary.Read(rdr.reader, binary.LittleEndian, &bo)
	if bo == 1 {
		rdr.ByteOrder = binary.BigEndian
	} else {
		rdr.ByteOrder = binary.LittleEndian
	}

	// Skip two bytes
	rdr.reader.Seek(2, 1)

	// Number of variables
	rdr.Nvar = rdr.read_int(nvar_width[rdr.FormatVersion])

	// Number of observations
	rdr.RowCount = rdr.read_int(row_count_width[rdr.FormatVersion])

	// Data label
	rdr.reader.Read(buf[0:81])
	rdr.DatasetLabel = string(partition(buf[0:81]))

	// Time stamp
	rdr.reader.Read(buf[0:18])
	rdr.TimeStamp = string(partition(buf[0:18]))

	return nil
}

func (rdr *StataReader) supported_version() bool {

	supported := false
	for _, v := range supported_dta_versions {
		if rdr.FormatVersion == v {
			supported = true
		}
	}
	return supported
}

// read_new_header reads a new-style xml header (versions 117+).
func (rdr *StataReader) read_new_header() error {

	buf := make([]byte, 500)
	var n8 uint8

	// <stata_dta><header><release>
	rdr.reader.Read(buf[0:28])
	if string(buf[0:11]) != "<stata_dta>" {
		return errors.New("Invalid Stata file")
	}

	// Stata file version
	rdr.reader.Read(buf[0:3])
	x, err := strconv.ParseUint(string(buf[0:3]), 0, 64)
	if err != nil {
		return err
	}
	rdr.FormatVersion = int(x)
	if !rdr.supported_version() {
		return errors.New("Invalid Stata dta format version")
	}

	// </release><byteorder>
	rdr.reader.Seek(21, 1)

	// Byte order
	rdr.reader.Read(buf[0:3])
	if string(buf[0:3]) == "MSF" {
		rdr.ByteOrder = binary.BigEndian
	} else {
		rdr.ByteOrder = binary.LittleEndian
	}

	// </byteorder><K>
	rdr.reader.Seek(15, 1)

	// Number of variables
	rdr.Nvar = rdr.read_int(nvar_width[rdr.FormatVersion])

	// </K><N>
	rdr.reader.Seek(7, 1)

	// Number of observations
	rdr.RowCount = rdr.read_int(row_count_width[rdr.FormatVersion])

	// </N><label>
	rdr.reader.Seek(11, 1)

	// Data set label
	w := rdr.read_uint(dataset_label_width[rdr.FormatVersion])
	rdr.reader.Read(buf[0:w])
	rdr.DatasetLabel = string(buf[0:w])

	// </label><timestamp>
	rdr.reader.Seek(19, 1)

	// Time stamp
	binary.Read(rdr.reader, rdr.ByteOrder, &n8)
	rdr.reader.Read(buf[0:n8])
	rdr.TimeStamp = string(buf[0:n8])

	// </timestamp></header><map> + 16 bytes
	rdr.reader.Seek(42, 1)

	// Map
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_vartypes)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_varnames)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_sortlist)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_formats)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_value_label_names)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_variable_labels)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_characteristics)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_data)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_strls)
	binary.Read(rdr.reader, rdr.ByteOrder, &rdr.seek_value_labels)

	return nil
}

func (rdr *StataReader) read_vartypes() {
	switch {
	case rdr.FormatVersion == 118:
		rdr.read_vartypes_16()
	case rdr.FormatVersion == 117:
		rdr.read_vartypes_16()
	case rdr.FormatVersion == 115:
		rdr.read_vartypes_8()
	case rdr.FormatVersion == 114:
		rdr.read_vartypes_8()
	default:
		panic(fmt.Sprintf("unknown format version %v in read_vartypes", rdr.FormatVersion))
	}
}

func (rdr *StataReader) read_vartypes_16() {
	rdr.reader.Seek(rdr.seek_vartypes+16, 0)
	rdr.VarTypes = make([]uint16, rdr.Nvar)
	for k := 0; k < int(rdr.Nvar); k++ {
		binary.Read(rdr.reader, rdr.ByteOrder, &rdr.VarTypes[k])
	}
}

func (rdr *StataReader) read_vartypes_8() {
	rdr.VarTypes = make([]uint16, rdr.Nvar)
	b := make([]byte, 1)
	for k := 0; k < int(rdr.Nvar); k++ {
		binary.Read(rdr.reader, rdr.ByteOrder, &b)
		rdr.VarTypes[k] = uint16(b[0])
	}
}

func (rdr *StataReader) translate_vartypes() {
	for k := 0; k < int(rdr.Nvar); k++ {
		switch {
		// strf
		case rdr.VarTypes[k] <= 244:
			continue
		case rdr.VarTypes[k] == 251:
			rdr.VarTypes[k] = 65530
		case rdr.VarTypes[k] == 252:
			rdr.VarTypes[k] = 65529
		case rdr.VarTypes[k] == 253:
			rdr.VarTypes[k] = 65528
		case rdr.VarTypes[k] == 254:
			rdr.VarTypes[k] = 65527
		case rdr.VarTypes[k] == 255:
			rdr.VarTypes[k] = 65526
		default:
			panic("unknown variable type %v in translate_vartypes")
		}
	}
}

func (rdr *StataReader) read_formats() {
	switch {
	case rdr.FormatVersion == 118:
		rdr.do_read_formats(57, true)
	case rdr.FormatVersion == 117:
		rdr.do_read_formats(57, true)
	case rdr.FormatVersion == 115:
		rdr.do_read_formats(49, false)
	case rdr.FormatVersion == 114:
		rdr.do_read_formats(49, false)
	default:
		panic(fmt.Sprintf("unknown format version %v in read_varnames", rdr.FormatVersion))
	}
}

func (rdr *StataReader) do_read_formats(bufsize int, seek bool) {

	buf := make([]byte, bufsize)
	if seek {
		rdr.reader.Seek(rdr.seek_formats+9, 0)
	}
	rdr.Formats = make([]string, rdr.Nvar)
	for k := 0; k < int(rdr.Nvar); k++ {
		rdr.reader.Read(buf)
		rdr.Formats[k] = string(partition(buf))
	}

	rdr.is_date = make([]bool, rdr.Nvar)
	for k := 0; k < int(rdr.Nvar); k++ {
		if strings.Index(rdr.Formats[k], "%td") == 0 {
			rdr.is_date[k] = true
		} else if strings.Index(rdr.Formats[k], "%tc") == 0 {
			rdr.is_date[k] = true
		}
	}
}

// Returns everything before the first null byte.
func partition(b []byte) []byte {
	for i, v := range b {
		if v == 0 {
			return b[0:i]
		}
	}
	return b
}

// read_varnames dispatches to the correct function for reading
// variable names for the dta file format.
func (rdr *StataReader) read_varnames() {
	switch {
	case rdr.FormatVersion == 118:
		rdr.do_read_varnames(129, true)
	case rdr.FormatVersion == 117:
		rdr.do_read_varnames(129, true)
	case rdr.FormatVersion == 115:
		rdr.do_read_varnames(33, false)
	case rdr.FormatVersion == 114:
		rdr.do_read_varnames(33, false)
	default:
		panic(fmt.Sprintf("unknown format version %v in read_varnames", rdr.FormatVersion))
	}
}

func (rdr *StataReader) do_read_varnames(bufsize int, seek bool) {
	buf := make([]byte, bufsize)
	if seek {
		rdr.reader.Seek(rdr.seek_varnames+10, 0)
	}
	rdr.column_names = make([]string, rdr.Nvar)
	for k := 0; k < int(rdr.Nvar); k++ {
		rdr.reader.Read(buf)
		rdr.column_names[k] = string(partition(buf))
	}
}

func (rdr *StataReader) read_value_label_names() {
	switch {
	case rdr.FormatVersion == 118:
		rdr.do_read_value_label_names(129, true)
	case rdr.FormatVersion == 117:
		rdr.do_read_value_label_names(129, true)
	case rdr.FormatVersion == 116:
		rdr.do_read_value_label_names(33, false)
	case rdr.FormatVersion == 115:
		rdr.do_read_value_label_names(33, false)
	default:
		panic(fmt.Sprintf("unknown format version %v in read_value_label_names", rdr.FormatVersion))
	}
}

func (rdr *StataReader) do_read_value_label_names(bufsize int, seek bool) {
	buf := make([]byte, bufsize)
	if seek {
		rdr.reader.Seek(rdr.seek_value_label_names+19, 0)
	}
	rdr.ValueLabelNames = make([]string, rdr.Nvar)
	for k := 0; k < int(rdr.Nvar); k++ {
		rdr.reader.Read(buf)
		rdr.ValueLabelNames[k] = string(partition(buf))
	}
}

func (rdr *StataReader) read_variable_labels() {
	switch {
	case rdr.FormatVersion == 118:
		rdr.do_read_variable_labels(321, true)
	case rdr.FormatVersion == 117:
		rdr.do_read_variable_labels(321, true)
	case rdr.FormatVersion == 115:
		rdr.do_read_variable_labels(81, false)
	case rdr.FormatVersion == 114:
		rdr.do_read_variable_labels(81, false)
	}
}

func (rdr *StataReader) do_read_variable_labels(bufsize int, seek bool) {
	buf := make([]byte, bufsize)
	if seek {
		rdr.reader.Seek(rdr.seek_variable_labels+17, 0)
	}
	rdr.ColumnNamesLong = make([]string, rdr.Nvar)
	for k := 0; k < int(rdr.Nvar); k++ {
		rdr.reader.Read(buf)
		rdr.ColumnNamesLong[k] = string(partition(buf))
	}
}

func (rdr *StataReader) read_value_labels() {

	vl := make(map[string]map[int32]string)

	buf := make([]byte, 321)
	rdr.reader.Seek(rdr.seek_value_labels+14, 0)
	var n int32
	var textlen int32

	for {
		rdr.reader.Read(buf[0:5])
		if string(buf[0:5]) != "<lbl>" {
			break
		}

		rdr.reader.Seek(4, 1)
		rdr.reader.Read(buf[0:129])
		labname := string(partition(buf[0:129]))
		rdr.reader.Seek(3, 1)

		binary.Read(rdr.reader, rdr.ByteOrder, &n)
		binary.Read(rdr.reader, rdr.ByteOrder, &textlen)

		off := make([]int32, n)
		val := make([]int32, n)

		for j := int32(0); j < n; j++ {
			binary.Read(rdr.reader, rdr.ByteOrder, &off[j])
		}

		for j := int32(0); j < n; j++ {
			binary.Read(rdr.reader, rdr.ByteOrder, &val[j])
		}

		buf = make([]byte, textlen)
		rdr.reader.Read(buf)

		vk := make(map[int32]string)

		for j := int32(0); j < n; j++ {
			vk[val[j]] = string(partition(buf[off[j]:]))
		}
		vl[labname] = vk

		// </lbl>
		rdr.reader.Seek(6, 1)
	}
	rdr.ValueLabels = vl
}

func (rdr *StataReader) read_strls() error {

	rdr.reader.Seek(rdr.seek_strls+7, 0)

	var v uint32
	var o uint64
	var t uint8
	var length uint32

	rdr.Strls = make(map[uint64]string)
	rdr.StrlsBytes = make(map[uint64][]byte)

	rdr.Strls[0] = ""

	buf3 := make([]byte, 3)

	for {
		rdr.reader.Read(buf3)
		if string(buf3) != "GSO" {
			break
		}

		binary.Read(rdr.reader, rdr.ByteOrder, &v)
		binary.Read(rdr.reader, rdr.ByteOrder, &o)
		binary.Read(rdr.reader, rdr.ByteOrder, &t)
		binary.Read(rdr.reader, rdr.ByteOrder, &length)

		// This is intended to create an 8-byte key that
		// matches the keys found in the actual data.  We then
		// insert the strls into the data set by key.
		var ky uint64
		ky = uint64(v) | (o << 16)

		buf := make([]byte, length)
		rdr.reader.Read(buf)

		if t == 130 {
			buf = partition(buf)
			rdr.Strls[ky] = string(buf)
		} else if t == 129 {
			rdr.StrlsBytes[ky] = buf
		}
	}

	return nil
}

// Read returns the given number of rows of data from the Stata data
// file.  The data are returned as an array of Series objects.  If
// rows is negative, the remainder of the file is read.
func (rdr *StataReader) Read(rows int) ([]*Series, error) {

	data := make([]interface{}, rdr.Nvar)
	missing := make([][]bool, rdr.Nvar)

	nval := int(rdr.RowCount) - rdr.rows_read
	if (rows >= 0) && (nval > rows) {
		nval = rows
	}

	for j := 0; j < int(rdr.Nvar); j++ {
		missing[j] = make([]bool, nval)
	}

	for j, t := range rdr.VarTypes {
		switch {
		case t <= 2045:
			data[j] = make([]string, nval)
		case t == 32768:
			if rdr.InsertStrls {
				data[j] = make([]string, nval)
			} else {
				data[j] = make([]uint64, nval)
			}
		case t == 65526:
			data[j] = make([]float64, nval)
		case t == 65527:
			data[j] = make([]float32, nval)
		case t == 65528:
			data[j] = make([]int32, nval)
		case t == 65529:
			data[j] = make([]int16, nval)
		case t == 65530:
			if rdr.InsertCategoryLabels {
				data[j] = make([]string, nval)
			} else {
				data[j] = make([]int8, nval)
			}
		}
	}

	if rdr.FormatVersion >= 117 {
		rdr.reader.Seek(rdr.seek_data+6, 0)
	}

	buf := make([]byte, 2045)
	for i := 0; i < nval; i++ {

		rdr.rows_read += 1
		if rdr.rows_read > int(rdr.RowCount) {
			break
		}

		for j := 0; j < rdr.Nvar; j++ {

			t := rdr.VarTypes[j]
			switch {
			case t <= 2045:
				// strf
				rdr.reader.Read(buf[0:t])
				data[j].([]string)[i] = string(partition(buf[0:t]))
			case t == 32768:
				if rdr.InsertStrls {
					var ptr uint64
					binary.Read(rdr.reader, rdr.ByteOrder, &ptr)
					data[j].([]string)[i] = rdr.Strls[ptr]
				} else {
					binary.Read(rdr.reader, rdr.ByteOrder, &(data[j].([]uint64)[i]))
				}
			case t == 65526:
				var x float64
				binary.Read(rdr.reader, rdr.ByteOrder, &x)
				data[j].([]float64)[i] = x
				// Lower bound in dta spec is out of range.
				if (x > 8.988e307) || (x < -8.988e307) {
					missing[j][i] = true
				}
			case t == 65527:
				var x float32
				binary.Read(rdr.reader, rdr.ByteOrder, &x)
				data[j].([]float32)[i] = x
				if (x > 1.701e38) || (x < -1.701e38) {
					missing[j][i] = true
				}
			case t == 65528:
				var x int32
				binary.Read(rdr.reader, rdr.ByteOrder, &x)
				data[j].([]int32)[i] = x
				if (x > 2147483620) || (x < -2147483647) {
					missing[j][i] = true
				}
			case t == 65529:
				var x int16
				binary.Read(rdr.reader, rdr.ByteOrder, &x)
				data[j].([]int16)[i] = x
				if (x > 32740) || (x < -32767) {
					missing[j][i] = true
				}
			case t == 65530:
				var x int8
				binary.Read(rdr.reader, rdr.ByteOrder, &x)
				if (x < -127) || (x > 100) {
					missing[j][i] = true
				}
				if !rdr.InsertCategoryLabels {
					data[j].([]int8)[i] = x
				} else {
					// bytes are converted to categorical.
					// We attempt to replace the value
					// with its appropriate category
					// label.  If this is not possible, we
					// just convert the byte value to a
					// string.

					// Check to see if we have label information
					labname := rdr.ValueLabelNames[j]
					mp, ok := rdr.ValueLabels[labname]
					if !ok {
						data[j].([]string)[i] = fmt.Sprintf("%v", x)
						continue
					}

					// Check to see if we have a label for this category
					v, ok := mp[int32(x)]
					if ok {
						data[j].([]string)[i] = v
					} else {
						data[j].([]string)[i] = fmt.Sprintf("%v", x)
					}
				}
			}
		}
	}

	if rdr.ConvertDates {
		for j, _ := range data {
			if rdr.is_date[j] {
				data[j] = rdr.do_convert_dates(data[j], rdr.Formats[j])
			}
		}
	}

	// Now that we have the raw data, convert it to a series.
	rdata := make([]*Series, len(data))
	var err error
	for j, v := range data {
		rdata[j], err = NewSeries(rdr.column_names[j], v, missing[j])
		if err != nil {
			return nil, err
		}
	}

	return rdata, nil
}

func (rdr *StataReader) do_convert_dates(v interface{}, format string) interface{} {

	vec, ok := v.([]int32)
	if !ok {
		panic("unable to handle raw type in date vector")
	}

	bt := time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC)

	rvec := make([]time.Time, len(vec))

	var tq time.Duration
	if strings.Index(format, "%td") == 0 {
		tq = time.Hour * 24
	} else if strings.Index(format, "%tc") == 0 {
		tq = time.Millisecond
	} else {
		panic("unable to handle format in date vector")
	}

	for j, v := range vec {
		d := time.Duration(v) * tq
		rvec[j] = bt.Add(d)
	}

	return rvec
}
