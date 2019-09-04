package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"monitoring/config"
	cfg "monitoring/config"
	"net/http"
	"regexp"
	"strings"
	"time"

	"project/warlog"
	"project/wpgx"
	"operator"

	"github.com/gocraft/web"
	"github.com/oxtoacart/bpool"
	blackfriday "gopkg.in/russross/blackfriday.v2"
)

var (
	RegxSql = regexp.MustCompile("\\$\\?")
	bufpool *bpool.BufferPool
)

func init() {
	bufpool = bpool.NewBufferPool(64) //TODO вынести в конфиг размер пула
}

type RegxCnt struct {
	Cnt int
}

func (r *RegxCnt) RegxReplace(str string) string {
	r.Cnt++
	return fmt.Sprintf("$%d", r.Cnt)
}

type Explain struct {
	Plan ExplainPlan `json:"Plan"`
}

type ExplainPlan struct {
	Row int `json:"Plan Rows"`
}

type JsonStatus struct {
	Status int    `json:"status"`
	Text   string `json:"text"`
}

type Context struct {
	User      userSession //Инфа по пользователю
	TFrom     time.Time   //дата для таблицы
	TTo       time.Time   //дата для таблицы
	TxID      string      //ID тракзации, но полное название транкзации брать через функцию TxID
	SessionID string      //ID сессии - ключ для memcashed
	Data      interface{}
	TX        wpgx.Dealer
	Bug       error
}

//Функция для проверки прав
func (c *Context) Granted(perm string) (access bool) {
	return c.User.Permissions[perm]
}

//Функция проверки прав доступа для редактирования абонентов
func (c *Context) SpecGranted(perm string) (access bool) {
	list := config.Config.AbonentChange.ChangeAccessList
	accessmap := make(map[string]bool, len(list))
	for i := range list {
		accessmap[list[i]] = true
	}
	return accessmap[perm]
}

func (c *Context) GrantedPage(rw web.ResponseWriter, req *web.Request, perm string) (err error) {
	//Получим сессию
	if err = c.getSession(rw, req); err != nil {
		warlog.Debug(func() interface{} {
			return err
		})
		return
	}

	if !c.Granted(perm) {
		return operator.ServerBug(ErrForbidden, c.TxID, operator.BugData{"err": "Недостаточно прав доступа", "page": req.URL})
	}
	return
}

func (c *Context) NameOperator() string {
	return cfg.Operators.GetSelfOperator().Name
}

func (c *Context) Version() string {
	return cfg.Config.VERSION
}

func (c *Context) SetTxID(rw web.ResponseWriter, req *web.Request, next web.NextMiddlewareFunc) {
	c.TxID = utils.UUID()

	next(rw, req)
}

func (c *Context) GetOperatorUrl() string {
	return cfg.Config.Operator.URL
}

func (c *Context) Home(rw web.ResponseWriter, req *web.Request) {
	http.Redirect(rw, req.Request, "/abonents", 301)
}

func (c *Context) getSession(rw web.ResponseWriter, req *web.Request) (err error) {
	defer func() {
		if err != nil {
			//Очищаем сессию в memcashed
			if c.SessionID != "" {
				cfg.MC.Delete(c.SessionID)
			}
			//Очищаем сессию в кукисах у пользователя
			clearSession(rw)
			replace_str := strings.Replace(req.URL.String(), "&", "@", -1)
			http.Redirect(rw, req.Request, fmt.Sprintf("/login?url=%s", replace_str), http.StatusFound)
		}
	}()
	cookie, err := req.Cookie("monitoring_session")

	if err != nil {
		return
	}

	cookieValue := make(map[string]string)
	err = CookieHandler.Decode("monitoring_session", cookie.Value, &cookieValue)
	if err != nil {
		return
	}

	if cookieValue["skey"] == "" {
		return operator.ClientBug(ErrForbidden, c.TxID, operator.BugData{"err": "Не найдено сессии по ключу skey", "page": req.URL})
	}

	c.SessionID = cookieValue["skey"]

	item, err := cfg.MC.Get(c.SessionID)
	if err != nil {
		return
	}

	var uSession userSession
	err = json.Unmarshal(item.Value, &uSession)
	if err != nil {
		return
	}

	if uSession.Expires.Before(time.Now()) {
		return operator.ClientBug(ErrForbidden, c.TxID, operator.BugData{"err": "Истекла сессия", "page": req.URL})
	}

	c.User = uSession
	warlog.Info(func() interface{} {
		return map[string]interface{}{
			"event":     "monitoring",
			"txid":      c.TxID,
			"sessionID": c.SessionID,
			"user":      c.User.User,
			"path":      req.URL.Path,
		}
	})
	c.TxID = fmt.Sprintf(`%s-%s`, c.TxID, c.SessionID)

	return
}

func (c *Context) CriticalErrors(rw web.ResponseWriter, req *web.Request, next web.NextMiddlewareFunc) {
	defer func() {
		if err := recover(); err != nil {
			c.ReturnJsonStatus(rw, 500, "Internal Server Error")
			warlog.ReportError(operator.ServerBug(operator.ErrUnknown, c.TxID, operator.BugData{"err": err}))
		}
	}()

	next(rw, req)
}

func (c *Context) ReturnJsonStatus(rw web.ResponseWriter, status int, text string) {
	c.Data = JsonStatus{Status: status, Text: text}
	c.encodeJson(rw)
}

func (c *Context) ReturnHTMLError(rw web.ResponseWriter, err error) {
	c.Data = err
	c.render(rw, TplError)
}

func (c *Context) ReturnHTML(rw web.ResponseWriter, fileName string) {
	c.render(rw, fileName)
}

func (c *Context) ReturnJson(rw web.ResponseWriter) {
	c.encodeJson(rw)
}

func (c *Context) encodeJson(rw web.ResponseWriter) {
	rw.Header().Set("Content-Type", "application/json")
	json, err := json.Marshal(c.Data)
	if err != nil {
		warlog.ReportError(err)
		http.Error(rw, "Internal Server Error", 500)
		return
	}
	rw.Write(json)
}

// Render - отрисовка шаблона
func (c *Context) render(rw web.ResponseWriter, fileName string) {
	if ManagerTemplates().Get(fileName) == nil {
		warlog.ReportError(fmt.Errorf("The template %s does not exist.", fileName))
		c.ReturnJsonStatus(rw, 500, fmt.Sprintf("The template %s does not exist.", fileName))
		return
	}

	buf := bufpool.Get()
	defer bufpool.Put(buf)

	t := ManagerTemplates().Get(fileName).Funcs(template.FuncMap{"Granted": c.Granted, "SpecGranted": c.SpecGranted})

	err := t.Execute(buf, c)
	if err != nil {
		warlog.ReportError(err)
		c.ReturnJsonStatus(rw, 500, "Internal Server Error")
		return
	}

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(rw)
}

// From - получение значения даты начала для шаблонизатора
func (c *Context) From() string {
	if c.TFrom.IsZero() {
		c.TFrom = time.Now().AddDate(-1, 0, 0)
	}
	return c.TFrom.Format(DBDateLayout)
}

// To - получение значения даты окончания для шаблонизатора
func (c *Context) To() string {
	if c.TTo.IsZero() {
		c.TTo = time.Now().Add(time.Duration(24) * time.Hour)
	}
	return c.TTo.Format(DBDateLayout)
}

//RenderReference - рендер справки
func (c *Context) RenderReference(link string, rw web.ResponseWriter) (err error) {
	ref, err := ioutil.ReadFile(link)
	if err != nil {
		return operator.ServerBug(operator.ErrUnknown, c.TxID, operator.BugData{"err": err})
	}
	rw.Write(blackfriday.Run(ref))
	return
}

//ВЫЗЫВАТЬ ТОЛЬКО В КОНКРЕТНОМ СУБРОУТЕ!!! Связано с  тем что в базовом роуте отдается статика и подготавливать транкзацию к бд для статики не нужно
func (c *Context) TxHandler(w web.ResponseWriter, r *web.Request, next web.NextMiddlewareFunc) {
	var err error

	if c.TX, err = cfg.PG.NewDealer(); err != nil {
		warlog.ReportError(operator.ServerBug(operator.ErrPGTransaction, c.TxID, operator.BugData{"err": err}))
		return
	}

	next(w, r)

	if err = c.TX.Jail(c.Bug == nil); err != nil {
		warlog.ReportError(operator.ServerBug(operator.ErrPGTransaction, c.TxID, operator.BugData{"err": err}))
		return
	}
}
