package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/fullstorydev/hauser/client"
	"github.com/fullstorydev/hauser/config"
	hausertest "github.com/fullstorydev/hauser/testing"
	"github.com/fullstorydev/hauser/warehouse"
	"github.com/pkg/errors"
)

func Ok(t *testing.T, err error, format string, a ...interface{}) {
	if err != nil {
		format += ": unexpected error: %s"
		a = append(a, err)
		t.Errorf(format, a...)
	}
}

func Assert(t *testing.T, condition bool, format string, a ...interface{}) {
	if !condition {
		t.Errorf(format, a...)
	}
}

func Equals(t *testing.T, expected, actual interface{}, format string, a ...interface{}) {
	if expected != actual {
		format += ": want %v (type %v), got %v (type %v)"
		a = append(a, expected, reflect.TypeOf(expected), actual, reflect.TypeOf(actual))
		t.Errorf(format, a...)
	}
}

func StrSliceEquals(t *testing.T, expected, actual []string, format string, a ...interface{}) {
	format += ": want %v, got %v (type %v)"
	a = append(a, expected, reflect.TypeOf(expected), actual, reflect.TypeOf(actual))

	if len(expected) != len(actual) {
		t.Errorf(format, a)
	}
	for i, e := range expected {
		if e != actual[i] {
			t.Errorf(format, a)
		}
	}
}

func TestHauser(t *testing.T) {

	testCases := []struct {
		name            string
		testdata        string
		freqSetting     int32
		expectedBundles int
	}{
		{
			name:            "base case",
			testdata:        "./testing/testdata/testdata.json",
			freqSetting:     48,
			expectedBundles: 5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conf := &config.Config{}
			fsClient := hausertest.NewMockDataExportClient(tc.freqSetting, tc.testdata)
			wh := hausertest.NewMockWarehouse()

			hauser := NewHauser(conf, fsClient, wh)
			err := hauser.Init()

			Ok(t, err, "failed to init")
			Assert(t, wh.Initialized, "expected warehouse to be initialized")

			numBundles, err := hauser.ProcessNext()
			Ok(t, err, "failed to process next bundles")
			Equals(t, tc.expectedBundles, numBundles, "wrong number of bundles processed")
			Equals(t, tc.expectedBundles, len(wh.UploadedFiles), "unexepected number of upload files")
			Equals(t, tc.expectedBundles, len(wh.DeletedFiles), "unexepected number of deleted files")
			Equals(t, tc.expectedBundles, len(wh.LoadedFiles), "unexepected number of loaded files")
			StrSliceEquals(t, wh.UploadedFiles, wh.LoadedFiles, "file mismatch")
			StrSliceEquals(t, wh.UploadedFiles, wh.DeletedFiles, "file mismatch")
		})
	}
}

func TestGetRetryInfo(t *testing.T) {
	testCases := []struct {
		err           error
		expDoRetry    bool
		expRetryAfter time.Duration
	}{
		{
			err:           errors.New("random error!"),
			expDoRetry:    true,
			expRetryAfter: defaultRetryAfterDuration,
		},
		{
			err:           client.StatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: 3 * time.Second},
			expDoRetry:    true,
			expRetryAfter: 3 * time.Second,
		},
		{
			err:           client.StatusError{StatusCode: http.StatusInternalServerError, RetryAfter: 3 * time.Second},
			expDoRetry:    true,
			expRetryAfter: 3 * time.Second,
		},
		{
			err:           client.StatusError{StatusCode: http.StatusServiceUnavailable, RetryAfter: 3 * time.Second},
			expDoRetry:    true,
			expRetryAfter: 3 * time.Second,
		},
		{
			err:           client.StatusError{StatusCode: http.StatusNotFound, RetryAfter: 3 * time.Second},
			expDoRetry:    false,
			expRetryAfter: defaultRetryAfterDuration,
		},
	}

	for i, tc := range testCases {
		doRetry, retryAfter := getRetryInfo(tc.err)
		if doRetry != tc.expDoRetry {
			t.Errorf("expected %t, got %t for doRetry on test case %d", tc.expDoRetry, doRetry, i)
		}
		if retryAfter != tc.expRetryAfter {
			t.Errorf("expected %v, got %v for doRetry on test case %d", tc.expRetryAfter, retryAfter, i)
		}
	}
}

func TestTransformExportJSONRecord(t *testing.T) {
	testCases := []struct {
		tableColumns []string
		rec          map[string]interface{}
		expResult    []string
	}{
		// no custom vars
		{
			tableColumns: []string{"eventtargettext", "pageduration", "customvars"},
			rec: map[string]interface{}{
				"EventTargetText": "Heyo!",
				"PageDuration":    42,
			},
			expResult: []string{"Heyo!", "42", `{}`},
		},
		// two custom vars
		{
			tableColumns: []string{"eventtargettext", "pageduration", "customvars"},
			rec: map[string]interface{}{
				"EventTargetText": "Heyo!",
				"PageDuration":    42,
				"myCustom_str":    "Heyo again!",
				"myCustom_num":    42,
			},
			expResult: []string{"Heyo!", "42", `{"myCustom_str":"Heyo again!","myCustom_num":42}`},
		},
		// missing column value for pageduration
		{
			tableColumns: []string{"eventtargettext", "pageduration", "customvars"},
			rec: map[string]interface{}{
				"EventTargetText": "Heyo!",
			},
			expResult: []string{"Heyo!", "", `{}`},
		},
		// additional columns in target table that are not in the export
		{
			tableColumns: []string{"eventtargettext", "pageduration", "customvars", "randomcolumnnotinexport"},
			rec: map[string]interface{}{
				"EventTargetText": "Heyo!",
				"PageDuration":    42,
			},
			expResult: []string{"Heyo!", "42", `{}`, ""},
		},
	}

	for i, tc := range testCases {
		wh := StubWarehouse{}
		result, err := TransformExportJSONRecord(&wh, tc.tableColumns, tc.rec)
		if err != nil {
			t.Errorf("Unexpected err %s on test case %d", err, i)
			continue
		}
		if len(result) != len(tc.expResult) {
			t.Errorf("Incorrect length of result; expected %d, got %d on test case %d", len(result), len(tc.expResult), i)
			continue
		}
		for j := range tc.expResult {
			if !compareTransformedStrings(t, tc.expResult[j], result[j]) {
				t.Errorf("Result mismatch; expected %s, got %s on test case %d, item %d", tc.expResult[j], result[j], i, j)
			}
		}
	}
}

func compareTransformedStrings(t *testing.T, str1, str2 string) bool {
	if str1 == str2 {
		return true
	}
	if len(str1) > 0 && str1[0] == '{' {
		return compareJSONStrings(t, str2, str2)
	}
	return false
}

func compareJSONStrings(t *testing.T, str1, str2 string) bool {
	// decode JSON
	var m1, m2 interface{}
	if err := json.Unmarshal([]byte(str1), &m1); err != nil {
		t.Fatalf("Could not unmarshal JSON string: %s", str1)
	}
	if err := json.Unmarshal([]byte(str2), &m2); err != nil {
		t.Fatalf("Could not unmarshal JSON string: %s", str2)
	}
	decoded1 := m1.(map[string]interface{})
	decoded2 := m2.(map[string]interface{})

	// compare decoded maps
	for key1, value1 := range decoded1 {
		value2, ok := decoded2[key1]
		if !ok || value1 != value2 {
			return false
		}
	}
	return true
}

type StubWarehouse struct {
	warehouse.Warehouse
}

func (sw *StubWarehouse) ValueToString(val interface{}, isTime bool) string {
	s := fmt.Sprintf("%v", val)
	if isTime {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t.Format(warehouse.RFC3339Micro)
	}
	return s
}
