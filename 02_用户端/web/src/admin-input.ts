// Parse decimal text directly into integer cents; never round invalid input.
export function parseMoneyCents(text: string): number {
  const value = text.trim()
  if (!/^(0|[1-9]\d*)(\.\d{1,2})?$/.test(value)) throw new Error('金额须为非负数，最多两位小数（不支持科学计数法）')
  const [yuan, fraction = ''] = value.split('.')
  const cents = BigInt(yuan) * 100n + BigInt(fraction.padEnd(2, '0'))
  if (cents > 99999999n) throw new Error('金额不能超过 999999.99 元')
  return Number(cents)
}

export function parseStockThreshold(text: string): number {
  if (!/^(0|[1-9]\d*)$/.test(text)) throw new Error('库存预警阈值须为非负整数')
  const value = Number(text)
  if (!Number.isSafeInteger(value) || value > 2147483647) throw new Error('库存预警阈值过大')
  return value
}
