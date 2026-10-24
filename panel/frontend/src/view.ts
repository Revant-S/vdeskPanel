export interface View {
  element: HTMLElement;
  // show runs each time the view is selected, so what it shows is never older than the click.
  show(): void;
}
