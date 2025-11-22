package wisun

import (
	"fmt"
	"strings"
	"testing"
	"time"

	gomock "github.com/golang/mock/gomock"
	"github.com/u-one/go-el-controller/transport"
)

// Copied from rl7023_client_test.go as they are unexported
type resp_RL7023_Rec struct {
	d string
	e error
}

func mock_RL7023_Rec(t *testing.T, m *transport.MockSerial, response []resp_RL7023_Rec) {
	t.Helper()

	respCnt := 0

	m.EXPECT().Send(gomock.Any()).DoAndReturn(func(cmd []byte) error {
		return nil
	}).AnyTimes()

	m.EXPECT().Recv().DoAndReturn(func() ([]byte, error) {
		if respCnt >= len(response) {
			return nil, fmt.Errorf("no more responses")
		}
		resp := response[respCnt].d
		err := response[respCnt].e
		respCnt++
		return []byte(resp), err
	}).AnyTimes()
}

func Test_RL7023_Send_Recovery(t *testing.T) {
	t.Parallel()

	// SNA Response (ESV=0x52 at index 10 of data)
	// Header: 10 81 00 01 02 88 01 05 FF 01 52 ...
	snaData := "1081000102880105FF015200"
	snaResponse := fmt.Sprintf("ERXUDP FE80::1 FE80::2 0E1A 0E1A 001C6400030C12A4 1 0 000C %s\r\n", snaData)

	t.Run("SNA triggers backoff", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		m := transport.NewMockSerial(ctrl)

		// Expect Send -> Event 21 -> OK -> ERXUDP (SNA)
		responses := []resp_RL7023_Rec{
			{"EVENT 21 2001:DB8::2 0 00\r\n", nil},
			{"OK\r\n", nil},
			{snaResponse, nil},
		}
		mock_RL7023_Rec(t, m, responses)

		c := &RL7023Client{
			serial:          m,
			panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
			backoffDuration: 1 * time.Millisecond, // Short backoff for test
		}

		_, err := c.Send([]byte("test"))
		if err == nil {
			t.Fatal("Expected error due to SNA, got nil")
		}
		if err.Error() != "received SNA" {
			t.Errorf("Expected 'received SNA', got '%v'", err)
		}
		if c.errorCount != 1 {
			t.Errorf("Expected errorCount 1, got %d", c.errorCount)
		}
	})

	t.Run("Generic error triggers backoff", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		m := transport.NewMockSerial(ctrl)

		// Expect Send -> Error
		m.EXPECT().Send(gomock.Any()).Return(nil)
		m.EXPECT().Recv().Return(nil, fmt.Errorf("generic error")).AnyTimes()

		c := &RL7023Client{
			serial:          m,
			panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
			backoffDuration: 1 * time.Millisecond,
		}

		_, err := c.Send([]byte("test"))
		if err == nil {
			t.Fatal("Expected error, got nil")
		}
		if c.errorCount != 1 {
			t.Errorf("Expected errorCount 1, got %d", c.errorCount)
		}
	})

	t.Run("Reconnect triggered after 5 errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		m := transport.NewMockSerial(ctrl)

		callOrder := 0
		m.EXPECT().Send(gomock.Any()).DoAndReturn(func(cmd []byte) error {
			s := string(cmd)
			if callOrder == 0 {
				// First send is the data
				if !strings.Contains(s, "SKSENDTO") {
					t.Errorf("Expected SKSENDTO, got %s", s)
				}
			} else if callOrder == 1 {
				// Second send should be SKTERM (from Reconnect -> Term)
				if !strings.Contains(s, "SKTERM") {
					t.Errorf("Expected SKTERM (Reconnect), got %s", s)
				}
			}
			callOrder++
			return nil
		}).AnyTimes()

		// Return generic error to avoid loop in sendCore
		m.EXPECT().Recv().Return(nil, fmt.Errorf("generic error")).AnyTimes()

		c := &RL7023Client{
			serial:          m,
			panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
			backoffDuration: 1 * time.Millisecond,
			errorCount:      5, // Pre-set to 5
		}

		c.Send([]byte("test"))

		if callOrder < 2 {
			t.Errorf("Expected at least 2 Send calls (Data + SKTERM), got %d", callOrder)
		}
		
		if c.errorCount != 6 {
			t.Errorf("Expected errorCount 6 (failed reconnect), got %d", c.errorCount)
		}
	})
}
