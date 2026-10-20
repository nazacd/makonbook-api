// What a question picture shows. The API fills it (internal/practice's
// card.go builds the same shape) and the page draws it: the Telegram group's
// pictures and the editor's Export image.

export type Choices = Record<string, string> | null;

/** A question in the content format (format.ts). */
export type Content = {
  passage: string | null;
  stem: string;
  choices: Choices;
  correct: string | null; // mcq
  accepted: string[] | null; // grid-in
};

/** The bank's own question, as SAT MAKON typed it: plain text with $ math
 * (Math) or a few HTML tags (R&W), and its screenshots. */
export type Bank = {
  passage: string | null;
  question: string;
  choices: Choices;
  answer: string; // a letter, or a grid-in's answers separated by commas
  images: Record<string, string>; // "question" or a letter → data URL
};

export type CardData = {
  kind: 'original' | 'fix' | 'question';
  place: string; // e.g. "DAY3 · Reading and Writing · Module 2 · Question 4"
  subject: 'math' | 'english';
  type: 'mcq' | 'grid_in';
  content?: Content; // fix and question
  base?: Content; // fix: the version it changes, to highlight what's new
  bank?: Bank; // original
  figures: Record<string, string>; // an image key in the content → data URL
};
