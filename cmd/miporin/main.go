package main

import (
	"context"
	"net/http"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	"github.com/bonavadeur/miporin/pkg/libs"
	"github.com/bonavadeur/miporin/pkg/miporin"
	"github.com/bonavadeur/miporin/pkg/scraper"
	"github.com/bonavadeur/miporin/pkg/yukari"
	"github.com/labstack/echo/v4"
)

var (
	KUBECONFIG        = miporin.Kubeconfig()
	OKASAN_SCRAPERS   = map[string]*scraper.OkasanScraper{}
	OKASAN_SCHEDULERS = map[string]*yukari.OkasanScheduler{}
)

func init() {
	scraper.OKASAN_SCRAPERS = OKASAN_SCRAPERS
	yukari.OKASAN_SCRAPERS = OKASAN_SCRAPERS
	yukari.OKASAN_SCHEDULERS = OKASAN_SCHEDULERS
}

func main() {
	bonalib.Log("Have a nice day, LAZYken")
	ctx := context.Background()

	// check license ahihi
	go libs.License(false)

	// start scraper
	go scraper.Scraper(OKASAN_SCRAPERS)

	// start scheduler
	if miporin.Cm2Bool("ikukantai-miporin-enable-yukari") {
		go yukari.Scheduler(OKASAN_SCHEDULERS)
	}

	// start echo server
	go server()

	// Blocks the main goroutine indefinitely by waiting on the Done() channel from the ctx context
	// Since the background context never completes, this effectively makes the program run indefinitely unless externally interrupted
	<-ctx.Done()
}

func server() {
	e := echo.New()
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Konnichiwa, Miporin-chan desu\n")
	})

	e.GET("/api/weight/:okasan/:kodomo", func(c echo.Context) error {
		okasanScraper, ok := OKASAN_SCRAPERS[c.Param("okasan")]
		if ok {
			kodomoScraper, ok := okasanScraper.Kodomo[c.Param("kodomo")]
			if ok {
				return c.JSON(http.StatusOK, kodomoScraper.Weight)
			} else {
				return c.JSON(http.StatusNotFound, "NotFound")
			}
		} else {
			return c.JSON(http.StatusNotFound, "NotFound")
		}
	})

	e.GET("/api/podcidr", func(c echo.Context) error {
		return c.JSON(http.StatusOK, miporin.GetPodsCIDRs())
	})

	e.GET("/api/state/:okasan/:kodomo", func(c echo.Context) error {
		okasanScheduler, ok := OKASAN_SCHEDULERS[c.Param("okasan")]
		if ok {
			bonalib.Log("Logging current state")
			kodomoScheduler, ok := okasanScheduler.Kodomo[c.Param("kodomo")]
			if ok {
				return c.JSON(http.StatusOK, kodomoScheduler.State)
			} else {
				return c.JSON(http.StatusNotFound, "NotFound")
			}
		} else {
			return c.JSON(http.StatusNotFound, "NotFound")
		}
	})

	e.GET("/api/state/:okasan/:kodomo/warmdisk", func(c echo.Context) error {
		okasanScheduler, ok := OKASAN_SCHEDULERS[c.Param("okasan")]
		if ok {
			kodomoScheduler, ok := okasanScheduler.Kodomo[c.Param("kodomo")]
			if ok {
				bonalib.Log("kodomo scheduler", kodomoScheduler)
				// kodomoScheduler.KodomoStateChan.WarmdiskSig()
				kodomoScheduler.StateChan.WarmDisk <- true
				return c.JSON(http.StatusOK, kodomoScheduler.State)
			} else {
				return c.JSON(http.StatusNotFound, "NotFound")
			}
		} else {
			return c.JSON(http.StatusNotFound, "NotFound")
		}
	})

	e.GET("/api/state/:okasan/:kodomo/warmcpu", func(c echo.Context) error {
		okasanScheduler, ok := OKASAN_SCHEDULERS[c.Param("okasan")]
		if ok {
			kodomoScheduler, ok := okasanScheduler.Kodomo[c.Param("kodomo")]
			if ok {
				bonalib.Log("kodomo scheduler", kodomoScheduler)
				// kodomoScheduler.KodomoStateChan.WarmdiskSig()
				kodomoScheduler.StateChan.WarmCPU <- true
				return c.JSON(http.StatusOK, kodomoScheduler.State)
			} else {
				return c.JSON(http.StatusNotFound, "NotFound")
			}
		} else {
			return c.JSON(http.StatusNotFound, "NotFound")
		}
	})

	e.Logger.Fatal(e.Start(":18080"))
}
