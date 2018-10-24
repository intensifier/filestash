package model

import (
	. "github.com/mickael-kerjean/nuage/server/common"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"net/smtp"
	"html/template"
	"strings"
	"time"
)

const PASSWORD_DUMMY = "{{PASSWORD}}"

type Proof struct {
	Id      string  `json:"id"`
	Key     string  `json:"key"`
	Value   string  `json:"-"`
	Message *string `json:"message,omitempty"`
	Error   *string `json:"error,omitempty"`
}

type Share struct {
	Id           string   `json:"id"`
	Backend      string   `json:"-"`
	Auth         string   `json:"auth,omitempty"`
	Path         string   `json:"path"`
	Password     *string  `json:"password,omitempty"`
	Users        *string  `json:"users,omitempty"`
	Expire       *int64   `json:"expire,omitempty"`
	Url          *string  `json:"url,omitempty"`
	CanShare     bool     `json:"can_share"`
	CanManageOwn bool     `json:"can_manage_own"`
	CanRead      bool     `json:"can_read"`
	CanWrite     bool     `json:"can_write"`
	CanUpload    bool     `json:"can_upload"`
}

func NewShare(id string) Share {
	return Share{
		Id: id,
	}
}

func (s Share) IsValid() (bool, error) {
	if s.Expire != nil {
		now := time.Now().UnixNano() / 1000000
		if now > *s.Expire {
			return false, NewError("Link has expired", 410)
		}
	}
	return true, nil
}

func (s *Share) MarshalJSON() ([]byte, error) {
	p := Share{
		s.Id,
		s.Backend,
		"",
		s.Path,
		func(pass *string) *string{
			if pass != nil {
				return NewString(PASSWORD_DUMMY)
			}
			return nil
		}(s.Password),
		s.Users,
		s.Expire,
		s.Url,
		s.CanShare,
		s.CanManageOwn,
		s.CanRead,
		s.CanWrite,
		s.CanUpload,
	}
	return json.Marshal(p)
}
func(s *Share) UnmarshallJSON(b []byte) error {
	var tmp map[string]interface{}
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}

	for key, value := range tmp {
		switch key {
		case "password": s.Password = NewStringpFromInterface(value)
		case "users": s.Users = NewStringpFromInterface(value)
		case "expire": s.Expire = NewInt64pFromInterface(value)
		case "url": s.Url = NewStringpFromInterface(value)
		case "can_share": s.CanShare = NewBoolFromInterface(value)
		case "can_manage_own": s.CanManageOwn = NewBoolFromInterface(value)
		case "can_read": s.CanRead = NewBoolFromInterface(value)
		case "can_write": s.CanWrite = NewBoolFromInterface(value)
		case "can_upload": s.CanUpload = NewBoolFromInterface(value)
		}
	}
	return nil
}

func ShareList(p *Share) ([]Share, error) {
	stmt, err := DB.Prepare("SELECT id, related_path, params FROM Share WHERE related_backend = ? AND related_path LIKE ? || '%' ")
	if err != nil {
		return nil, err
	}
	rows, err := stmt.Query(p.Backend, p.Path)
	if err != nil {
		return nil, err
	}
	sharedFiles := []Share{}
	for rows.Next() {
		var a Share
		var params []byte
		rows.Scan(&a.Id, &a.Path, &params)
		json.Unmarshal(params, &a)		
		sharedFiles = append(sharedFiles, a)
	}
	rows.Close()
	return sharedFiles, nil
}

func ShareGet(p *Share) error {
	stmt, err := DB.Prepare("SELECT id, related_path, params, auth FROM share WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	row := stmt.QueryRow(p.Id)
	var str []byte
	if err = row.Scan(&p.Id, &p.Path, &str, &p.Auth); err != nil {
		if err == sql.ErrNoRows {
			return NewError("Not Found", 404)
		}
		return err
	}
	json.Unmarshal(str, &p)
	return nil
}

func ShareUpsert(p *Share) error {
	if p.Password != nil {
		if *p.Password == PASSWORD_DUMMY {
			var copy Share
			copy.Id = p.Id
			ShareGet(&copy);
			p.Password = copy.Password
		} else {
			hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(*p.Password), bcrypt.DefaultCost)
			p.Password = NewString(string(hashedPassword))
		}
	}

	stmt, err := DB.Prepare("INSERT INTO Location(backend, path) VALUES($1, $2)")
	if err != nil {
		return err
	}
	_, err = stmt.Exec(p.Backend, p.Path)
	if err != nil {
		throw := true
		if ferr, ok := err.(sqlite3.Error); ok == true && ferr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey {
			throw = false
		}
		if throw == true {
			return err
		}
	}

	stmt, err = DB.Prepare("INSERT INTO Share(id, related_backend, related_path, params, auth) VALUES($1, $2, $3, $4, $5) ON CONFLICT(id) DO UPDATE SET related_backend = $2, related_path = $3, params = $4")
	if err != nil {
		return err
	}
	j, _ := json.Marshal(&struct {
        Password     *string  `json:"password,omitempty"`
		Users        *string  `json:"users,omitempty"`
		Expire       *int64   `json:"expire,omitempty"`
		Url          *string  `json:"url,omitempty"`
		CanShare     bool     `json:"can_share"`
		CanManageOwn bool     `json:"can_manage_own"`
		CanRead      bool     `json:"can_read"`
		CanWrite     bool     `json:"can_write"`
		CanUpload    bool     `json:"can_upload"`
    }{
		Password: p.Password,
		Users: p.Users,
		Expire: p.Expire,
		Url: p.Url,
		CanShare: p.CanShare,
		CanManageOwn: p.CanManageOwn,
		CanRead: p.CanRead,
		CanWrite: p.CanWrite,
		CanUpload: p.CanUpload,
    })
	_, err = stmt.Exec(p.Id, p.Backend, p.Path, j, p.Auth)
	return err
}

func ShareDelete(p *Share) error {
	stmt, err := DB.Prepare("DELETE FROM Share WHERE id = ? AND related_backend = ?")
	if err != nil {
		return err
	}
	_, err = stmt.Exec(p.Id, p.Backend)
	return err
}

func ShareProofVerifier(ctx *App, s Share, proof Proof) (Proof, error) {
	p := proof

	if proof.Key == "password" {
		if s.Password == nil {
			return p, NewError("No password required", 400)
		}
		time.Sleep(1000 * time.Millisecond)
		if err := bcrypt.CompareHashAndPassword([]byte(*s.Password), []byte(proof.Value)); err != nil {
			return p, NewError("Invalid Password", 403)
		}
		p.Value = *s.Password
	}

	if proof.Key == "email" {
		// find out if user is authorized
		if s.Users == nil {
			return p, NewError("Authentication not required", 400)
		}
		var user *string
		for _, possibleUser := range strings.Split(*s.Users, ",") {
			if proof.Value == strings.Trim(possibleUser, " ") {
				user = &proof.Value
			}
		}
		if user == nil {
			time.Sleep(1000 * time.Millisecond)
			return p, NewError("No access was provided", 400)
		}

		// prepare the verification code
		stmt, err := DB.Prepare("INSERT INTO Verification(key, code) VALUES(?, ?)");
		if err != nil {
			return p, err
		}
		code := RandomString(4)
		if _, err := stmt.Exec("email::" + proof.Value, code); err != nil {
			return p, err
		}

		// Prepare message
		var b bytes.Buffer
		t := template.New("email")
		t.Parse(TmplEmailVerification())
		t.Execute(&b, struct{
			Code string
		}{code})

		p.Key = "code"
		p.Value = ""
		p.Message = NewString("We've sent you a message with a verification code")

		// Send email
		addr := fmt.Sprintf(
			"%s:%d",
			ctx.Config.Get("email.server").String(),
			ctx.Config.Get("email.port").Int(),
		)
		mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
		subject := "Subject: Your verification code\n"
		msg := []byte(subject + mime + "\n" + b.String())
		auth := smtp.PlainAuth(
			"",
			ctx.Config.Get("email.username").String(),
			ctx.Config.Get("email.password").String(),
			ctx.Config.Get("email.server").String(),
		)

		if err := smtp.SendMail(addr, auth, ctx.Config.Get("email.from").String(), []string{proof.Value}, msg); err != nil {
			log.Println("ERROR: ", err)
			log.Println("Verification code: " + code)
			return p, NewError("Couldn't send email", 500)
		}
	}

	if proof.Key == "code" {
		// find key for given code
		stmt, err := DB.Prepare("SELECT key FROM Verification WHERE code = ? AND expire > datetime('now')")
		if err != nil {
			return p, NewError("Not found", 404)
		}
		row := stmt.QueryRow(proof.Value)
		var key string
		if err = row.Scan(&key); err != nil {
			if err == sql.ErrNoRows {
				stmt.Close()
				p.Key = "email"
				p.Value = ""
				return p, NewError("Not found", 404)
			}
			stmt.Close()
			return p, err
		}
		stmt.Close()

		// cleanup current attempt so that it isn't used for malicious purpose
		if stmt, err = DB.Prepare("DELETE FROM Verification WHERE code = ?"); err == nil {
			stmt.Exec(proof.Value)
			stmt.Close()
		}
		p.Key = "email"
		p.Value = strings.TrimPrefix(key, "email::")
	}

	return p, nil
}

func ShareProofGetAlreadyVerified(req *http.Request, ctx *App) []Proof {
	var p []Proof
	var cookieValue string

	c, _ := req.Cookie(COOKIE_NAME_PROOF)
	if c == nil {
		return p
	}
	cookieValue = c.Value
	if len(cookieValue) > 500 {
		return p
	}
	j, err := DecryptString(ctx.Config.Get("general.secret_key").String(), cookieValue)
	if err != nil {
		return p
	}
	_ = json.Unmarshal([]byte(j), &p)
	return p
}

func ShareProofGetRequired(s Share) []Proof {
	var p []Proof
	if s.Password != nil {
		p = append(p, Proof{Key: "password", Value: *s.Password})
	}
	if s.Users != nil {
		p = append(p, Proof{Key: "email", Value: *s.Users})
	}
	return p
}

func ShareProofCalculateRemainings(ref []Proof, mem []Proof) []Proof {
	var remainingProof []Proof

	for i := 0; i < len(ref); i++ {
		keep := true
		for j := 0; j < len(mem); j++ {
			if shareProofAreEquivalent(ref[i], mem[j]) {
				keep = false
				break;
			}
		}
		if keep {
			remainingProof = append(remainingProof, ref[i])
		}
	}

	return remainingProof
}


func shareProofAreEquivalent(ref Proof,  p Proof) bool {
	if ref.Key != p.Key {
		return false
	}
	for _, chunk := range strings.Split(ref.Value, ",") {
		chunk = strings.Trim(chunk, " ")
		if p.Id == Hash(ref.Key + "::" + chunk) {
			return true
		}
	}
	return false
}

func TmplEmailVerification() string {
	return `
<!doctype html>
<html>
  <head>
    <meta name="viewport" content="width=device-width" />
    <meta http-equiv="Content-Type" content="text/html; charset=UTF-8" />
    <title>Nuage code</title>
    <style>
      /* -------------------------------------
          GLOBAL RESETS
      ------------------------------------- */
      img {
        border: none;
        -ms-interpolation-mode: bicubic;
        max-width: 100%; }
      body {
        background-color: #f6f6f6;
        font-family: sans-serif;
        -webkit-font-smoothing: antialiased;
        font-size: 14px;
        line-height: 1.4;
        margin: 0;
        padding: 0;
        -ms-text-size-adjust: 100%;
        -webkit-text-size-adjust: 100%; }
      table {
        border-collapse: separate;
        mso-table-lspace: 0pt;
        mso-table-rspace: 0pt;
        width: 100%; }
        table td {
          font-family: sans-serif;
          font-size: 14px;
          vertical-align: top; }
      /* -------------------------------------
          BODY & CONTAINER
      ------------------------------------- */
      .body {
        background-color: #f6f6f6;
        width: 100%; }
      /* Set a max-width, and make it display as block so it will automatically stretch to that width, but will also shrink down on a phone or something */
      .container {
        display: block;
        Margin: 0 auto !important;
        /* makes it centered */
        max-width: 450px;
        padding: 10px;
        width: 580px; }
      /* This should also be a block element, so that it will fill 100% of the .container */
      .content {
        box-sizing: border-box;
        display: block;
        Margin: 0 auto;
        max-width: 450px;
        padding: 10px; }
      /* -------------------------------------
          HEADER, FOOTER, MAIN
      ------------------------------------- */
      .main {
        background: #ffffff;
        border-radius: 3px;
        width: 100%; }
      .wrapper {
        box-sizing: border-box;
        padding: 20px; }
      .content-block {
        padding-bottom: 10px;
        padding-top: 10px;
      }
      .footer {
        clear: both;
        Margin-top: 10px;
        text-align: center;
        width: 100%; }
        .footer td,
        .footer p,
        .footer span,
        .footer a {
          color: #999999;
          font-size: 12px;
          text-align: center; }
      /* -------------------------------------
          TYPOGRAPHY
      ------------------------------------- */
      h1,
      h2,
      h3,
      h4 {
        color: #000000;
        font-family: sans-serif;
        font-weight: 400;
        line-height: 1.4;
        margin: 0;
        margin-bottom: 30px; }
      h1 {
        font-size: 35px;
        font-weight: 300;
        text-align: center;
        text-transform: capitalize; }
      p,
      ul,
      ol {
        font-family: sans-serif;
        font-size: 14px;
        font-weight: normal;
        margin: 0;
        margin-bottom: 15px; }
        p li,
        ul li,
        ol li {
          list-style-position: inside;
          margin-left: 5px; }
      a {
        color: #3498db;
        text-decoration: underline; }
      /* -------------------------------------
          BUTTONS
      ------------------------------------- */
      .btn {
        box-sizing: border-box;
        width: 100%; }
        .btn > tbody > tr > td {
          padding-bottom: 15px; }
        .btn table {
          width: auto; }
        .btn table td {
          background-color: #ffffff;
          border-radius: 5px;
          text-align: center; }
        .btn a {
          background-color: #ffffff;
          border: solid 1px #3498db;
          border-radius: 5px;
          box-sizing: border-box;
          color: #3498db;
          cursor: pointer;
          display: inline-block;
          font-size: 14px;
          font-weight: bold;
          margin: 0;
          padding: 12px 25px;
          text-decoration: none;
          text-transform: capitalize; }
      .btn-primary table td {
        background-color: #3498db; }
      .btn-primary a {
        background-color: #3498db;
        border-color: #3498db;
        color: #ffffff; }
      /* -------------------------------------
          OTHER STYLES THAT MIGHT BE USEFUL
      ------------------------------------- */
      .last {
        margin-bottom: 0; }
      .first {
        margin-top: 0; }
      .align-center {
        text-align: center; }
      .align-right {
        text-align: right; }
      .align-left {
        text-align: left; }
      .clear {
        clear: both; }
      .mt0 {
        margin-top: 0; }
      .mb0 {
        margin-bottom: 0; }
      .preheader {
        color: transparent;
        display: none;
        height: 0;
        max-height: 0;
        max-width: 0;
        opacity: 0;
        overflow: hidden;
        mso-hide: all;
        visibility: hidden;
        width: 0; }
      .powered-by a {
        text-decoration: none; }
      hr {
        border: 0;
        border-bottom: 1px solid #f6f6f6;
        Margin: 20px 0; }
      /* -------------------------------------
          RESPONSIVE AND MOBILE FRIENDLY STYLES
      ------------------------------------- */
      @media only screen and (max-width: 490px) {
        table[class=body] h1 {
          font-size: 28px !important;
          margin-bottom: 10px !important; }
        table[class=body] p,
        table[class=body] ul,
        table[class=body] ol,
        table[class=body] td,
        table[class=body] span,
        table[class=body] a {
          font-size: 16px !important; }
        table[class=body] .wrapper,
        table[class=body] .article {
          padding: 10px !important; }
        table[class=body] .content {
          padding: 0 !important; }
        table[class=body] .container {
          padding: 0 !important;
          width: 100% !important; }
        table[class=body] .main {
          border-left-width: 0 !important;
          border-radius: 0 !important;
          border-right-width: 0 !important; }
        table[class=body] .btn table {
          width: 100% !important; }
        table[class=body] .btn a {
          width: 100% !important; }
        table[class=body] .img-responsive {
          height: auto !important;
          max-width: 100% !important;
          width: auto !important; }}
      /* -------------------------------------
          PRESERVE THESE STYLES IN THE HEAD
      ------------------------------------- */
      @media all {
        .ExternalClass {
          width: 100%; }
        .ExternalClass,
        .ExternalClass p,
        .ExternalClass span,
        .ExternalClass font,
        .ExternalClass td,
        .ExternalClass div {
          line-height: 100%; }
        .apple-link a {
          color: inherit !important;
          font-family: inherit !important;
          font-size: inherit !important;
          font-weight: inherit !important;
          line-height: inherit !important;
          text-decoration: none !important; }
        .btn-primary table td:hover {
          background-color: #34495e !important; }
        .btn-primary a:hover {
          background-color: #34495e !important;
          border-color: #34495e !important; } }
    </style>
  </head>
  <body class="">
    <table border="0" cellpadding="0" cellspacing="0" class="body">
      <tr>
        <td>&nbsp;</td>
        <td class="container">
          <div class="content">

            <!-- START CENTERED WHITE CONTAINER -->
            <span class="preheader">Your code to login</span>
            <table class="main">

              <!-- START MAIN CONTENT AREA -->
              <tr>
                <td class="wrapper">
                  <table border="0" cellpadding="0" cellspacing="0">
                    <tr>
                      <td>
                        <h2 style="font-weight:100;margin:0">Your verification code is: <strong>{{.Code}}</strong></h2>
                      </td>
                    </tr>
                  </table>
                </td>
              </tr>

            <!-- END MAIN CONTENT AREA -->
            </table>

            <!-- START FOOTER -->
            <div class="footer">
              <table border="0" cellpadding="0" cellspacing="0">
                <tr>
                  <td class="content-block powered-by">
                    Powered by <a href="http://github.com/mickael-kerjean/nuage">Nuage</a>.
                  </td>
                </tr>
              </table>
            </div>
            <!-- END FOOTER -->

          <!-- END CENTERED WHITE CONTAINER -->
          </div>
        </td>
        <td>&nbsp;</td>
      </tr>
    </table>
  </body>
</html>
`
}