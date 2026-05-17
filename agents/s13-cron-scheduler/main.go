package main

import (
	"fmt"
	"time"
)

// main is a tiny offline demo. We register a daily-8am-Shanghai schedule and
// step a frozen UTC clock through 25 one-hour ticks, printing which ticks
// fire. The output should show exactly one fire per simulated day at the UTC
// instant that corresponds to 08:00 Asia/Shanghai (00:00 UTC).
//
// No goroutines, no time.Sleep — the loop drives `now` forward by hand. This
// is the same pattern we'd use in a unit test, but in main() it serves as a
// smoke check that the parser, the scheduler, and the timezone math all
// agree.
func main() {
	sched, err := Parse("0 8 * * *", "Asia/Shanghai")
	if err != nil {
		fmt.Println("parse error:", err)
		return
	}
	fmt.Printf("registered schedule %q in %s (next run after now in UTC = %s)\n",
		sched.Expression, sched.Timezone, mustNext(sched, time.Now().UTC()))

	scheduler := NewScheduler()
	if err := scheduler.Add("daily-digest", sched); err != nil {
		fmt.Println("add error:", err)
		return
	}

	// Step the clock from a fixed UTC moment in 1-hour increments across one
	// full day. We pick a starting time that's clearly NOT yet at the fire
	// boundary so we can see the difference.
	start := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	fmt.Printf("\n=== ticking from %s for 25 hours ===\n", start.Format(time.RFC3339))
	for i := 0; i < 25; i++ {
		now := start.Add(time.Duration(i) * time.Hour)
		due := scheduler.Tick(now)
		if len(due) > 0 {
			localShanghai := now.In(sched.loc).Format("2006-01-02 15:04 MST")
			fmt.Printf("[hour %2d UTC=%s local=%s] FIRES: %v\n",
				i, now.Format("15:04 MST"), localShanghai, due)
		} else {
			fmt.Printf("[hour %2d UTC=%s] (nothing due)\n", i, now.Format("15:04 MST"))
		}
	}
}

// mustNext is a main-only helper that lets us print the next run inline
// without a verbose if-err-return; on error we just stringify it so the demo
// never panics.
func mustNext(sch *CronSchedule, now time.Time) string {
	t, err := sch.NextRun(now)
	if err != nil {
		return fmt.Sprintf("ERROR(%v)", err)
	}
	return t.Format(time.RFC3339)
}
