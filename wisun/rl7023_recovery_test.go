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

	lastCmd := ""
	respCnt := -1

	m.EXPECT().Send(gomock.Any()).DoAndReturn(func(cmd []byte) error {
		lastCmd = string(cmd)
		respCnt = -1
		return nil
	}).AnyTimes()

	m.EXPECT().Recv().DoAndReturn(func() ([]byte, error) {
		if respCnt == -1 {
			respCnt++
			return []byte(lastCmd), nil
		}
		if respCnt >= len(response) {
			return nil, fmt.Errorf("no more responses")
		}
		resp := response[respCnt].d
		err := response[respCnt].e
		respCnt++
		return []byte(resp), err
	}).AnyTimes()
}

func mockRL7023Script(t *testing.T, m *transport.MockSerial, response []string) *[]string {
	t.Helper()

	sent := []string{}
	respCnt := 0

	m.EXPECT().Send(gomock.Any()).DoAndReturn(func(cmd []byte) error {
		sent = append(sent, string(cmd))
		return nil
	}).AnyTimes()

	m.EXPECT().Recv().DoAndReturn(func() ([]byte, error) {
		if respCnt >= len(response) {
			return nil, fmt.Errorf("no more responses")
		}
		resp := response[respCnt]
		respCnt++
		return []byte(resp), nil
	}).AnyTimes()

	return &sent
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

		// Expect echo -> Event 21 -> OK -> ERXUDP (SNA)
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
				// Reconnect now delegates SKTERM to Connect.
				if !strings.Contains(s, "SKTERM") {
					t.Errorf("Expected SKTERM (Connect), got %s", s)
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
			bRouteID:        "test-id",
			bRoutePW:        "test-password",
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

func Test_RL7023_SendFailureKeepsResponseSynchronized(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	m := transport.NewMockSerial(ctrl)

	responses := []string{
		"SKSENDTO 1 2001:DB8::2 0E1A 1 0 0004 \r\n",
		"EVENT 21 2001:DB8::2 0 01\r\n",
		"OK\r\n",
		"\r\n",
		"SKSENDTO 1 2001:DB8::2 0E1A 1 0 0004 \r\n",
		"EVENT 21 2001:DB8::2 0 00\r\n",
		"OK\r\n",
		"\r\n",
		"ERXUDP FE80::1 FE80::2 0E1A 0E1A 001C6400030C12A4 1 0 0012 1081000102880105FF017201E704000001F8\r\n",
	}
	mockRL7023Script(t, m, responses)

	c := &RL7023Client{
		serial:          m,
		panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
		backoffDuration: 0,
	}

	if _, err := c.Send([]byte("test")); err == nil {
		t.Fatal("expected EVENT 21 failure")
	}

	got, err := c.Send([]byte("test"))
	if err != nil {
		t.Fatalf("second send failed after draining response: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected ERXUDP response after resynchronization")
	}
}

func Test_RL7023_SendFailureSkipsInterleavedEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	m := transport.NewMockSerial(ctrl)

	responses := []string{
		"SKSENDTO 1 2001:DB8::2 0E1A 1 0 0004 \r\n",
		"EVENT 21 2001:DB8::2 0 01\r\n",
		"EVENT 28 2001:DB8::2 0\r\n",
		"OK\r\n",
		"\r\n",
		"SKSENDTO 1 2001:DB8::2 0E1A 1 0 0004 \r\n",
		"EVENT 21 2001:DB8::2 0 00\r\n",
		"OK\r\n",
		"\r\n",
		"ERXUDP FE80::1 FE80::2 0E1A 0E1A 001C6400030C12A4 1 0 0012 1081000102880105FF017201E704000001F8\r\n",
	}
	mockRL7023Script(t, m, responses)

	c := &RL7023Client{
		serial:          m,
		panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
		backoffDuration: 0,
	}

	if _, err := c.Send([]byte("test")); err == nil {
		t.Fatal("expected EVENT 21 failure")
	}

	got, err := c.Send([]byte("test"))
	if err != nil {
		t.Fatalf("second send failed after skipping interleaved event: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected ERXUDP response after resynchronization")
	}
}

func Test_RL7023_TermWaitsForSessionEndBeforeNextCommand(t *testing.T) {
	tests := []struct {
		name      string
		responses []string
	}{
		{
			name: "EVENT 27 after asynchronous traffic",
			responses: []string{
				"SKTERM\r\n",
				"OK\r\n",
				"EVENT 21 FE80::2 0 00\r\n",
				"ERXUDP FE80::1 FE80::2 0E1A 0E1A 001C6400030C12A4 1 0 0004 00000000\r\n",
				"EVENT 27 FE80::2\r\n",
				"SKSETPWD C test-password\r\n",
				"OK\r\n",
			},
		},
		{
			name: "EVENT 28",
			responses: []string{
				"SKTERM\r\n",
				"OK\r\n",
				"EVENT 28 FE80::2\r\n",
				"SKSETPWD C test-password\r\n",
				"OK\r\n",
			},
		},
		{
			name: "FAIL ER10",
			responses: []string{
				"SKTERM\r\n",
				"FAIL ER10\r\n",
				"SKSETPWD C test-password\r\n",
				"OK\r\n",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			m := transport.NewMockSerial(ctrl)
			mockRL7023Script(t, m, tc.responses)

			c := &RL7023Client{serial: m}
			c.Term()
			if err := c.SetBRoutePassword("test-password"); err != nil {
				t.Fatalf("SKSETPWD failed after Term: %v", err)
			}
		})
	}
}

func Test_RL7023_SendPropagatesWriteError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	m := transport.NewMockSerial(ctrl)

	m.EXPECT().Send(gomock.Any()).Return(fmt.Errorf("serial write failed"))

	c := &RL7023Client{
		serial:          m,
		panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
		backoffDuration: 0,
	}

	_, err := c.Send([]byte("test"))
	if err == nil || err.Error() != "serial write failed" {
		t.Fatalf("expected serial write error, got %v", err)
	}
}

func Test_RL7023_ReconnectResynchronizesAndContinuesAfterER10(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	m := transport.NewMockSerial(ctrl)

	responses := []string{
		// Failed data send and its trailing command-completion lines.
		"SKSENDTO 1 2001:DB8::2 0E1A 1 0 0004 \r\n",
		"EVENT 21 2001:DB8::2 0 01\r\n",
		"OK\r\n",
		"\r\n",
		// Connect's single SKTERM is allowed to return FAIL ER10.
		"SKTERM\r\n",
		"FAIL ER10\r\n",
		"SKSETPWD C test-password\r\n",
		"OK\r\n",
		"SKSETRBID test-id\r\n",
		"OK\r\n",
		"SKSCAN 2 FFFFFFFF 4 0 \r\n",
		"OK\r\n",
		"EVENT 20 FE80::1 0\r\n",
		"EPANDESC\r\n",
		" Channel:39\r\n",
		" Channel Page:09\r\n",
		" Pan ID:80DE\r\n",
		" Addr:C0F94500409680DE\r\n",
		" LQI:28\r\n",
		" Side:0\r\n",
		" PairID:00E70300\r\n",
		"EVENT 22 FE80::1 0\r\n",
		"SKSREG S2 39\r\n",
		"OK\r\n",
		"SKSREG S3 80DE\r\n",
		"OK\r\n",
		"SKLL64 C0F94500409680DE\r\n",
		"FE80::2\r\n",
		"SKJOIN FE80::2\r\n",
		"OK\r\n",
		"EVENT 25 FE80::2 0\r\n",
		// A command after reconnect must use the new session cleanly.
		"SKSENDTO 1 FE80::2 0E1A 1 0 0004 \r\n",
		"EVENT 21 FE80::2 0 00\r\n",
		"OK\r\n",
		"\r\n",
		"ERXUDP FE80::1 FE80::2 0E1A 0E1A 001C6400030C12A4 1 0 0012 1081000102880105FF017201E704000001F8\r\n",
	}
	sent := mockRL7023Script(t, m, responses)

	c := &RL7023Client{
		serial:          m,
		panDesc:         PanDesc{IPV6Addr: "2001:DB8::2"},
		bRouteID:        "test-id",
		bRoutePW:        "test-password",
		errorCount:      5,
		backoffDuration: 0,
		reconnectDelay:  0,
	}

	if _, err := c.Send([]byte("test")); err == nil {
		t.Fatal("expected the original send to fail")
	}
	if c.errorCount != 0 {
		t.Fatalf("expected successful reconnect to reset error count, got %d", c.errorCount)
	}
	if !c.joined {
		t.Fatal("expected reconnect to complete Join")
	}

	got, err := c.Send([]byte("test"))
	if err != nil {
		t.Fatalf("send after reconnect failed: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected response after reconnect")
	}

	termCount := 0
	for _, cmd := range *sent {
		if strings.HasPrefix(cmd, "SKTERM") {
			termCount++
		}
	}
	if termCount != 1 {
		t.Fatalf("expected exactly one SKTERM during reconnect, got %d (%v)", termCount, *sent)
	}

	order := []string{"SKSENDTO", "SKTERM", "SKSETPWD", "SKSETRBID", "SKSCAN", "SKSREG S2", "SKSREG S3", "SKLL64", "SKJOIN", "SKSENDTO"}
	orderIndex := 0
	for _, cmd := range *sent {
		if orderIndex < len(order) && strings.HasPrefix(cmd, order[orderIndex]) {
			orderIndex++
		}
	}
	if orderIndex != len(order) {
		t.Fatalf("unexpected reconnect command order, matched %d/%d: %v", orderIndex, len(order), *sent)
	}
}
