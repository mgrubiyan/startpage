// Where the startpage server runs. Change this if you deploy it elsewhere.
const STARTPAGE_URL = 'http://localhost:8080/';

// replace() keeps the extension page out of the tab's back history.
location.replace(STARTPAGE_URL);
