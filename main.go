package main

import (
	"flag"
	"log"
	. "project/config"
	handler "project/handlers"
	"project/handlers/abonents"

	"project/warlog"

	"net/http"
	"os"
	"path"
	"syscall"
	"time"

	"github.com/gocraft/web"
)

var (
	//router     = mux.NewRouter()
	version    string
	date_build string
	numcommit  string
)

func main() {
	var cfg_name string

	flag.StringVar(&cfg_name, "cfg", "./config.yml", "Имя файла конфигурации")
	flag.BoolVar(&warlog.DebugMode, "debug", true, "Подключение отладочного вывода. Может увеличить нагрузку")
	flag.Parse()

	// Аварийный сброс по сигналам выхода
	warlog.CloseAt(func(caller os.Signal) {
		log.Fatalf("Аварийный выход по сигналу: `%s`\n", caller)
	}, os.Interrupt, os.Kill, syscall.SIGSTOP, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGABRT)

	// Базовый логгер в STD
	stdlg := warlog.NewStdLogger(warlog.LvlDebug, os.Stdout, 2<<15, 30*time.Second)
	defer stdlg.Close()
	warlog.AddLogger("main", stdlg)

	warlog.Info(func() interface{} {
		return map[string]interface{}{
			"Дата билда": date_build,
			"Коммит":     numcommit,
		}
	})

	err := ParseConfig(cfg_name)
	if err != nil {
		warlog.ReportError(err)
		return
	}

	Config.VERSION = version

	currentRoot, _ := os.Getwd()
	router := web.New(handler.Context{}).
		Middleware((*handler.Context).CriticalErrors).
		Middleware((*handler.Context).SetTxID).
		Middleware(web.StaticMiddleware(path.Join(currentRoot, "static"), web.StaticOption{Prefix: "/static/"})).
		Get("/", (*handler.Context).Home)

	router.Get("/login", handler.Login)
	router.Get("/auth", handler.Auth)
	router.Get("/login_out", handler.LoginOut)

	abonentsRouter := router.Subrouter(abonents.AbonentsHandler{}, "/abonents")
	abonentsRouter.Middleware((*abonents.AbonentsHandler).GrantedThisPage)
	abonentsRouter.Middleware((*abonents.AbonentsHandler).TxPreparation)
	abonentsRouter.Get("/", (*abonents.AbonentsHandler).Page)
	abonentsRouter.Get("/:view/", (*abonents.AbonentsHandler).View)
	abonentsRouter.Post("/:act/", (*abonents.AbonentsHandler).Acts)

	if err := http.ListenAndServe(Config.Http.LocalURL, router); err != nil {
		warlog.ReportError(err)
	}
}
