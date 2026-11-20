package main

import _ "embed"

// managePage is the manage page on the local port: every link with its
// record, clicks and history, and the controls to change them. It's one
// self-contained file, served with a CSP that allows nothing from outside
// (see handleManagePage); it signs in with a one-time code from
// `shortlinker manage` and keeps the session it trades the code for.
//
//go:embed manage.html
var managePage []byte
