# learninggo
Simple projects for me to use to learn Go

learninggo/tickets
    A simple movie ticket library.
    Initial impl. does not use a DB (don't know how, yet).
learninggo/tickets/sample_server
    A trivial HTTP server to present learninggo/tickets as a service.
learninggo/theatre
    Models a movie theatre, using learninggo/tickets/sample_server
    as its back-end.  Note that this is NOT a webapp or graphical model.
    It just writes results to a log file, stdout, and stderr.
    It's primary purpose is to exercise learninggo/tickets in a
    multitasking way.

CAUTION!  As of 01FEB2017, the exchanges, movies, showings, seats, and windows
          options need to be kept in sync between tickets/sample_server and
          theatre command line options.  If you bring them up with non-matching
          values, results are unpredictable.

          A uniform startup, inherited startup, or config query would be nice
          to have to remedy this situation.

