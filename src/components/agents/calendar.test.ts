import assert from 'node:assert/strict'
import { calendarMonths } from './calendar.ts'
import { addDays, isRollingPeriod, periodRange, weekdayIndex } from './usage.ts'

for (const today of ['2026-10-02', '2026-01-01', '2024-03-01', '2025-03-01', '2026-12-31']) {
  for (const period of ['30d', '60d', '90d'] as const) {
    const count = parseInt(period, 10)
    const range = periodRange(period, today)
    const months = calendarMonths(range.from, range.to)
    const dates = months.flatMap(month => month.days.filter(day => day?.inRange).map(day => day!.date))
    assert.equal(dates.length, count, `${period} ending ${today} includes exactly ${count} days`)
    assert.equal(new Set(dates).size, count, 'each day appears once, including across month/year boundaries')
    assert.equal(dates[0], addDays(today, 1 - count))
    assert.equal(dates.at(-1), today)
    assert.deepEqual(dates, Array.from({ length: count }, (_, i) => addDays(range.from, i)))
    for (const month of months) {
      assert.equal(month.days.length % 7, 0, 'complete Monday-first week rows')
      month.days.forEach((day, index) => {
        if (day) {
          assert.equal(weekdayIndex(day.date), index % 7)
          assert.equal(day.date.slice(0, 7), month.month)
        }
      })
    }
  }
}
assert.equal(periodRange('30d', '2024-03-01').from, '2024-02-01', 'leap day counts')
assert.equal(periodRange('30d', '2025-03-01').from, '2025-01-31', 'non-leap February crosses three months')
assert.equal(periodRange('2025', '2026-10-02').to, '2025-12-31', 'calendar years retain their existing ranges')
assert.equal(isRollingPeriod('30d'), true)
assert.equal(isRollingPeriod('2026'), false)
assert.equal(isRollingPeriod('300d'), false)
console.log('Trailing calendars: inclusive ranges, leap years, year boundaries and weekday alignment passed')
