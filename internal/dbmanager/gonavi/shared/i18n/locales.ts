export const DEFAULT_LANGUAGE = "zh-CN";

export const SUPPORTED_LANGUAGES = ["zh-CN", "en-US"] as const;

export type SupportedLanguage = (typeof SUPPORTED_LANGUAGES)[number];

export const isSupportedLanguage = (
  value: unknown,
): value is SupportedLanguage =>
  typeof value === "string" &&
  (SUPPORTED_LANGUAGES as readonly string[]).includes(value);

export const resolveLanguage = (value: unknown): SupportedLanguage =>
  isSupportedLanguage(value) ? value : DEFAULT_LANGUAGE;
