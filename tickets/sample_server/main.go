/*****************************************************************************

'tickets' sells movie tickets and manages promotional gifts ("goodies").

This program is an optional http server wrapped around the 'tickets' package.
You can use it if you want to make 'tickets' available as a stand-alone Web
service.  It will then have a JSON interface.

The following URLs are supported:

    POST  /tickets/init/
        This URL is accessed with POST.  The request is sent in JSON format:
	    {
	        "MaxExchanges"   : <max.# exchanges>,
		"MaxMovies"      : <max.# movies>,
		"MaxShowings"    : <max.# showings per movie per day>,
		"MaxSeats"       : <max.# seats per showing>,
		"MaxWindows"     : <max.# open ticket windows>,
            }
	If an error occurs, it is sent back in the response body.
	HTTP 204 (http.StatusNoContent) or 400 (http.StatusBadRequest).

    POST  /tickets/sell/<window_number>
        This URL is accessed with POST.  The request is sent in JSON format:
            {
                "TicketRequests" : [ [ <movie#>, <showning#> ], ... ],
                "PaymentInfo"    : <can be anything -- validation not implemented>,
                "LocalTime"      : <can be anything -- not currently used>
            }
        The reply is also sent back in JSON format:
            {
                "tickets"        :   [ { <struct Ticket expressed as a JSON map> }, ... ],
                "receipt"        :   { <struct Receipt expressed as a JSON map> }
            }
	and you get HTTP 200 (http.StatusOK) on success.

    GET or POST  /tickets/exchange/<ticket_number>/<old_goodie>/<new_goodie>
        There is no additional payload with this URL.  Use GET or POST.
        There is no reply data (get HTTP 204 (http.StatusNoContent) on success).

    POST  /tickets/stop/<optional_return_code>
        This URL is access with POST.  A text/plain request body is optional.
	If a non-empty request body is present, it is emitted  *AS-IS*  to
	the log (i.e. it is NOT in JSON format).

See the doc. in tickets.go for application details.

*****************************************************************************/

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/d-m-w/learninggo/tickets"
	//"tickets"
)

const (
	ServerPort = "1811" // for tickets service on localhost

	LogFileBase = "log/tickets."
)

var L *log.Logger

// main starts and runs the sample tickets server.
// As of Issue # 4, all configuration is supplied by the client,
// whose first request must be 'init' (all other requests will
// fail with an error, until init is requested).
func main() {
	logFileName := LogFileBase + time.Now().Format("2006-01-02t15-04-05z-0700")
	logFile, logErr := os.Create(logFileName)
	defer logFile.Close()
	if logErr == nil {
		//// For now, don't run this.  Depending on user's umask, this might
		//// actually INCREASE access to the logfile, instead of protecting it.
		//logErr = logFile.Chmod(0644)
	}
	if logErr != nil {
		// log setup failed, so write this one to the default log, instead
		log.Fatalf("ticketServer aborting:  Error setting up log file '%s':  %v", logFileName, logErr)
	}
	L = log.New(logFile, "ticketServer:  ", log.Ldate|log.Ltime|log.Lshortfile) //? need '*'

	// End common logging init.

	http.HandleFunc("/tickets/init/", initTicketService)
	http.HandleFunc("/tickets/sell/", sellTickets)
	http.HandleFunc("/tickets/exchange/", handleExchange)
	http.HandleFunc("/tickets/stop/", stopTicketService)
	L.Fatal(http.ListenAndServe("localhost:"+ServerPort, nil))
} // main

// initTicketService is an adapter between the http Handler protocol and the
// ticketing system's Init function.  The URL format is:
//     /tickets/init/
// and the request body must contain the configuration parameters in JSON format:
//	    {
//	        "MaxExchanges"   : <max.# exchanges>,
//		"MaxMovies"      : <max.# movies>,
//		"MaxShowings"    : <max.# showings per movie per day>,
//		"MaxSeats"       : <max.# seats per showing>,
//		"MaxWindows"     : <max.# open ticket windows>,
//          }
// There is no response body on success, so you get HTTP 204 (http.StatusNoContent)
// Failures get HTTP 400 (http.StatusBadRequest)
func initTicketService(w http.ResponseWriter, rqst *http.Request) {

	// Init all fields to invalid values.
	// This means that initialization will fail, unless the
	// client provides good data for all supported fields.
	requestData := struct {
		MaxExchanges int // <max.# exchanges>,
		MaxMovies    int // <max.# movies>,
		MaxShowings  int // <max.# showings per movie per day>,
		MaxSeats     int // <max.# seats per showing>,
		MaxWindows   int // <max.# open ticket windows>,
	}{-1, -1, -1, -1, -1}

	L.Printf("initTicketService called for %v\n", rqst.URL)

	jparser := json.NewDecoder(rqst.Body)

	if err := jparser.Decode(&requestData); err != nil {
		L.Printf("Request '%s' failed:  data not in JSON format:  %v\n", rqst.URL.Path, err)
		http.Error(w, "data not in JSON format", http.StatusBadRequest)
		return
	}

	if err := tickets.Init(L, requestData.MaxExchanges, requestData.MaxMovies, requestData.MaxShowings, requestData.MaxSeats, requestData.MaxWindows); err != nil {
		L.Printf("initTicketService failed:  ticket system initialization failed:  %v\n", err)
		http.Error(w, fmt.Sprintf("%v", err), http.StatusBadRequest)
		return
	}

	http.Error(w, "initialized", http.StatusNoContent)
	return

} // initTicketService

// handleExchange is an adapter between the http Handler protocol and the
// ticketing system's Exchange function.  The URL format is:
//     /tickets/exchange/<ticket_number>/<old_goodie>/<new_goodie>
// and there is no additional response body.  Access the URL with HTTP GET.
//
// If there are no errors in the request and the specified ticket allows the
// exchange, then the specified item is reported as exchanged for the specified
// replacement, in the ticketing system.
//
// Returns HTTP 400 on errors and HTTP 204 on success.
// No payload is ever sent back.
func handleExchange(w http.ResponseWriter, rqst *http.Request) {
	const (
		PPTickNum   = 3 // where's the ticket number in the URL.Path?
		PPOldGoodie = 4 // where's the goodie to be exchanged, in the URL.Path?
		PPNewGoodie = 5 // where's the requested replacement item, in the URL.Path?
	)

	L.Printf("handleExchange called for %v\n", rqst.URL)

	pathParts := strings.Split(rqst.URL.Path, "/")
	tickNum, err := strconv.Atoi(pathParts[PPTickNum])
	if err != nil {
		L.Printf("Request '%s' failed:  ticket number invalid:  %v\n", rqst.URL.Path, err)
		http.Error(w, "ticket number invalid", http.StatusBadRequest)
		return
	}

	err = tickets.Exchange(tickNum, pathParts[PPOldGoodie], pathParts[PPNewGoodie])
	if err != nil {
		L.Printf("Request '%s' failed:  error from tickets.Exchange:  %v\n", rqst.URL.Path, err)
		http.Error(w, fmt.Sprintf("%v", err), http.StatusBadRequest)
		return
	}

	// I don't think calling "Error" is really the right way to report success,
	// but I can't find any other way to send back a 204 ...
	http.Error(w, "exchanged", http.StatusNoContent)
	return
} // handleExchange

// sellTickets is an adapter between the http Handler protocol and the
// ticketing system's Sell function.
//
// URLs are accessed with HTTP POST.  URL format
//   /tickets/sell/<window_number>
//
// JSON data format:
//   {
//     "TicketRequests" : [ [<movie#>, <showing#>], [<movie#>, <showing#>], ... ],
//     "PaymentInfo"    : { <any number of fields with any contents> },
//     "LocalTime"      : <anything>
//   }
//
// One possible GO data format:
//   var requestData struct {
//      // Use the same case for the variable names as the JSON map keys.
//      TicketRequests [][2]int               // { movie #, showing # }
//      PaymentInfo    map[string]interface{} // not currently implemented
//      LocalTime      interface{}            // not currently implemented
//   }
//
// If there are no errors, then the Sell function's response converted to JSON
// format and returned, with an HTTP 200 status code.
//
// If an error occurs, then HTTP 400 or 500 is returned.
func sellTickets(w http.ResponseWriter, rqst *http.Request) {
	const (
		PPWindow = 3 // where's the Window# in the URL.Path?
	)
	var requestData struct {
		// Use the same case for the variable names as the JSON map keys.
		TicketRequests [][2]int               // { movie #, showing # }
		PaymentInfo    map[string]interface{} // not currently implemented
		LocalTime      interface{}            // not currently implemented
	}

	L.Printf("sellTickets called for %v\n", rqst.URL)

	jparser := json.NewDecoder(rqst.Body)

	if err := jparser.Decode(&requestData); err != nil {
		L.Printf("Request '%s' failed:  data not in JSON format:  %v\n", rqst.URL.Path, err)
		http.Error(w, "data not in JSON format", http.StatusBadRequest)
		return
	}

	window, winerr := strconv.Atoi(strings.Split(rqst.URL.Path, "/")[PPWindow])
	if winerr != nil {
		L.Printf("Request '%s' failed:  window number invalid:  %v\n", rqst.URL.Path, winerr)
		http.Error(w, "window number invalid", http.StatusBadRequest)
		return
	}

	ticks, rcpt, err := tickets.Sell(window, requestData.TicketRequests, requestData.PaymentInfo, requestData.LocalTime)
	if err != nil {
		L.Printf("Request '%s':  error from tickets.Sell after processing %d ticket requests:  %v\n", rqst.URL.Path, len(ticks), err)
		// Use zero-length string for body argument, so that we can
		// still write JSON data to the body, if there is any.  If
		// we wrote a non-empty string here, then the caller would
		// get a JSON unmarshalling error.
		switch err {
		case tickets.ErrNoMoreTickets:
			http.Error(w, "", http.StatusTooManyRequests)
		default:
			http.Error(w, "", http.StatusBadRequest)
		}
	}

	var responseData struct {
		// All fields must be exported (capitalized), to be visible to json.
		Ticks []tickets.Ticket
		Rcpt  tickets.Receipt
	}
	responseData.Ticks = ticks
	responseData.Rcpt = rcpt
	L.Printf("sellTickets window %d responseData\n%+v\n", window, responseData)
	//jcoder := json.NewEncoder(w)
	//if err := jcoder.Encode(responseData); err != nil {
	jbuffer, err := json.Marshal(responseData)
	if err != nil {
		L.Printf("Request '%s' failed:  error marshalling response data to JSON:  %v\n", rqst.URL.Path, err)
		http.Error(w, "error marshalling response data to JSON", http.StatusInternalServerError)
		return
	}
	L.Printf("Marshalled responseData is %d bytes:\n'%s'\n", len(jbuffer), bytes.NewBuffer(jbuffer).String())
	w.Write(jbuffer)
	// Note:  http.ResponseWriter doesn't have a Close() method, so can't do that.
	return
} // sellTickets

// stopTicketService shuts down the tickets server.
//
// URLs are accessed with HTTP POST.  URL format
//   /tickets/stop/<optional_return_code>
//
// Request body is optional.
// If present, it must be unescaped
//   Content-Type: text/plain
// Any such data will be copied as-is to the server's log.
//bytes
// If a URL parsing error occurs, then HTTP 400 or 500 is returned.
//
// HTTP 204 (http.StatusNoContent) means that no errors were found
// and os.Exit(<optional_return_code_or_0> is about to be issued.
// It does not guarantee that os.Exit will succeed.
func stopTicketService(w http.ResponseWriter, rqst *http.Request) {
	const (
		PPRetCd = 3 // where's the Window# in the URL.Path?
	)
	var requestData bytes.Buffer
	var retCd int

	L.Printf("stopTicketService called for %v\n", rqst.URL)

	requestBytes, err := ioutil.ReadAll(rqst.Body)

	if err != nil {
		L.Printf("stopTicketService request '%s' substituting default message:  unable to read message text from rqst.Body:  %v\n", rqst.URL.Path, err)
		requestData = *bytes.NewBufferString("<client shutdown message unreadable>")
	} else {
		requestData = *bytes.NewBuffer(requestBytes)
	}

	switch pathParts := strings.Split(rqst.URL.Path, "/"); {
	case len(pathParts) > PPRetCd:
		retCd, err = strconv.Atoi(pathParts[PPRetCd])
		if err != nil {
			L.Printf("stopTicketService request '%s' failed:  rawRetCd number invalid:  %v\n", rqst.URL.Path, err)
			http.Error(w, "server return code not an integer", http.StatusBadRequest)
			return
		}
	default:
		retCd = 0
	}

	L.Printf("stopTicketService shuttting down tickets server with return code %d\n%s\n", retCd, requestData)
	os.Exit(retCd)

} // stopTicketService
