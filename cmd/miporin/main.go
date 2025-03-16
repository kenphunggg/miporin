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

	// MIPORIN API
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Konnichiwa, Miporin-chan desu\n")
	})

	// GET MIPORIN WEIGHT MATRIX
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

	// GET PODCIDR
	e.GET("/api/podcidr", func(c echo.Context) error {
		return c.JSON(http.StatusOK, miporin.GetPodsCIDRs())
	})

	// API TO CHANGE POD TO [COLD] STATE
	e.GET("/state/:okasan/:kodomo/cold/podname/:name", func(c echo.Context) error {
		okasanScheduler, ok := OKASAN_SCHEDULERS[c.Param("okasan")]
		if ok {
			kodomoScheduler, ok := okasanScheduler.Kodomo[c.Param("kodomo")]
			if ok {
				bonalib.Info("Logging current state for pod in ksvc", kodomoScheduler.Name)
				dup := false
				for podmonitor_name, _ := range kodomoScheduler.PodMonitor {
					if c.Param("name") == podmonitor_name {
						bonalib.Warn("Pod monitor", podmonitor_name, "have been created")
						dup = true
					}
				}
				if !dup {
					newStatePod := yukari.NewPodMonitor(c.Param("name"), kodomoScheduler.SleepTime) //Create new instance of statepod
					newStatePod.Kodomo = kodomoScheduler                                            // Link that instance to kodomo
					kodomoScheduler.PodMonitor[newStatePod.Name] = newStatePod                      // Link Kodomo to that instance
					yukari.SchedulePodState(newStatePod)
					// newStatePod.StateChan.WarmDisk <- true
					bonalib.Log("Pod monitor", kodomoScheduler.PodMonitor[c.Param("name")].Name, "created!")
				}
				return c.JSON(http.StatusOK, kodomoScheduler.PodMonitor[c.Param("name")].State)
			} else {
				bonalib.Warn("Ksvc", kodomoScheduler.Name, "not found")
				return c.JSON(http.StatusNotFound, "NotFound")
			}
		} else {
			bonalib.Warn("Okasan", okasanScheduler.Name, "not found")
			return c.JSON(http.StatusNotFound, "NotFound")
		}
	})

	// API TO CHANGE POD TO [WARMCPU] STATE
	e.GET("/api/state/:okasan/:kodomo/warmcpu/podname/:name/node/:nodename", func(c echo.Context) error {
		okasanScheduler, ok := OKASAN_SCHEDULERS[c.Param("okasan")]
		if ok {
			kodomoScheduler, ok := okasanScheduler.Kodomo[c.Param("kodomo")]
			if ok {
				podstate, ok := kodomoScheduler.PodMonitor[c.Param("name")]
				if ok {
					bonalib.Info("Logging current state for pod", c.Param("name"), "in ksvc", kodomoScheduler.Name)
					podstate.NodeName = c.Param("nodename")
					bonalib.Log(podstate.Name, "podname", podstate.NodeName, "nodename")
					podstate.StateChan.WarmCPU <- true
					bonalib.Log("TEST")
					return c.JSON(http.StatusOK, podstate.State)
				} else {
					bonalib.Warn("Pod", podstate.Name, "in ksvc", kodomoScheduler.Name, "not found")
					return c.JSON(http.StatusNotFound, "NotFound")
				}
			} else {
				bonalib.Warn("Ksvc", kodomoScheduler.Name, "not found")
				return c.JSON(http.StatusNotFound, "NotFound")
			}
		} else {
			bonalib.Warn("Okasan", okasanScheduler.Name, "not found")
			return c.JSON(http.StatusNotFound, "NotFound")
		}
	})

	e.Logger.Fatal(e.Start(":18080"))
}
