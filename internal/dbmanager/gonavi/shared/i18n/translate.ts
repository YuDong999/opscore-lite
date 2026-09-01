import { DEFAULT_LANGUAGE, resolveLanguage, type SupportedLanguage } from "./locales";
import { messages } from "./messages";

export type I18nParams = Record<string, string | number | boolean | null | undefined>;

const interpolate = (template: string, params?: I18nParams): string => {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, key) => {
    const value = params[key];
    return value === undefined || value === null ? match : String(value);
  });
};

export const translate = (
  key: string,
  params?: I18nParams,
  language: SupportedLanguage | string = DEFAULT_LANGUAGE,
): string => {
  const resolvedLanguage = resolveLanguage(language);
  const template =
    (messages[resolvedLanguage] as Record<string, string>)[key] ??
    (messages[DEFAULT_LANGUAGE] as Record<string, string>)[key];
  return template ? interpolate(template, params) : key;
};
