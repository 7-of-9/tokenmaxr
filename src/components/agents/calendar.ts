import { addDays, mondayOf, nextMonth, weekdayIndex } from './usage.ts'

export interface CalendarMonth {
  month: string
  /** Monday-first weeks. Nulls are outside this month; dimmed dates are outside the selected range. */
  days: Array<{ date: string; inRange: boolean } | null>
}

export function calendarMonths(from: string, to: string): CalendarMonth[] {
  const months: CalendarMonth[] = []
  for (let start = `${from.slice(0, 7)}-01`; start <= to; start = nextMonth(start)) {
    const end = addDays(nextMonth(start), -1)
    const gridEnd = addDays(end, 6 - weekdayIndex(end))
    const days: CalendarMonth['days'] = []
    for (let date = mondayOf(start); date <= gridEnd; date = addDays(date, 1)) {
      days.push(date < start || date > end ? null : { date, inRange: date >= from && date <= to })
    }
    months.push({ month: start.slice(0, 7), days })
  }
  return months
}
